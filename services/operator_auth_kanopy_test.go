package services

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

const testKid = "test-key-1"
const testOperatorGroup = "10gen-test-operators"

// testRSAKey is generated once per test run — 2048-bit RSA is slow to generate.
var (
	testKeyOnce sync.Once
	testRSAKey  *rsa.PrivateKey
)

func getTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testKeyOnce.Do(func() {
		var err error
		testRSAKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic("test RSA key generation: " + err.Error())
		}
	})
	return testRSAKey
}

// makeJWKSServer starts an httptest.Server that serves the given public key as
// a minimal JWKS document. Cleans up automatically via t.Cleanup.
func makeJWKSServer(t *testing.T, pub *rsa.PublicKey, kid string) *httptest.Server {
	t.Helper()
	nBytes := pub.N.Bytes()
	eBytes := big.NewInt(int64(pub.E)).Bytes()
	body, err := json.Marshal(map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA",
			"kid": kid,
			"n":   base64.RawURLEncoding.EncodeToString(nBytes),
			"e":   base64.RawURLEncoding.EncodeToString(eBytes),
		}},
	})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// makeTestCache returns a kanopyJWKSCache pointed at the given httptest server.
func makeTestCache(srv *httptest.Server) *kanopyJWKSCache {
	return newKanopyJWKSCache(srv.URL)
}

// signToken creates a signed RS256 JWT using the provided claims and key.
func signToken(t *testing.T, key *rsa.PrivateKey, kid string, claims corpSecureClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	require.NoError(t, err)
	return signed
}

// validUserClaims returns a baseline set of claims for a human user token.
func validUserClaims(groups ...string) corpSecureClaims {
	return corpSecureClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    kanopyIssuer,
			Subject:   "jdoe",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
		Email:  "jdoe@mongodb.com",
		Groups: groups,
	}
}

func TestValidateKanopyJWT(t *testing.T) {
	key := getTestKey(t)
	srv := makeJWKSServer(t, &key.PublicKey, testKid)
	cache := makeTestCache(srv)

	tests := []struct {
		name        string
		token       func() string
		wantRole    OperatorRole
		wantErrFrag string
	}{
		{
			name: "operator group member gets RoleOperator",
			token: func() string {
				return signToken(t, key, testKid, validUserClaims(testOperatorGroup, "10gen-everyone"))
			},
			wantRole: RoleOperator,
		},
		{
			name: "user not in operator group gets RoleWriter",
			token: func() string {
				return signToken(t, key, testKid, validUserClaims("10gen-everyone"))
			},
			wantRole: RoleWriter,
		},
		{
			name: "empty token rejected",
			token: func() string {
				return ""
			},
			wantErrFrag: "empty token",
		},
		{
			name: "wrong issuer rejected",
			token: func() string {
				c := validUserClaims()
				c.Issuer = "evil.example.com"
				return signToken(t, key, testKid, c)
			},
			wantErrFrag: "verify token",
		},
		{
			name: "expired token rejected",
			token: func() string {
				c := validUserClaims()
				c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
				c.IssuedAt = jwt.NewNumericDate(time.Now().Add(-3 * time.Hour))
				return signToken(t, key, testKid, c)
			},
			wantErrFrag: "verify token",
		},
		{
			name: "HS256 alg rejected (alg confusion)",
			token: func() string {
				// Sign with the HMAC method using the public key bytes as the
				// secret — this is the classic alg-confusion attack vector.
				hmacToken := jwt.NewWithClaims(jwt.SigningMethodHS256, validUserClaims())
				hmacToken.Header["kid"] = testKid
				signed, err := hmacToken.SignedString(key.N.Bytes())
				require.NoError(t, err)
				return signed
			},
			wantErrFrag: "verify token",
		},
		{
			name: "unknown kid rejected (no key fallback)",
			token: func() string {
				return signToken(t, key, "unknown-kid", validUserClaims())
			},
			wantErrFrag: "verify token",
		},
		{
			name: "mesh-internal scp claim rejected",
			token: func() string {
				c := validUserClaims()
				c.Scp = []string{"mesh-internal"}
				return signToken(t, key, testKid, c)
			},
			wantErrFrag: "non-user principal rejected",
		},
		{
			name: "spiffe:// subject rejected",
			token: func() string {
				c := validUserClaims()
				c.Subject = "spiffe://cluster.local/ns/docs/sa/some-service"
				return signToken(t, key, testKid, c)
			},
			wantErrFrag: "non-user principal rejected",
		},
		{
			name: "missing email rejected",
			token: func() string {
				c := validUserClaims()
				c.Email = ""
				return signToken(t, key, testKid, c)
			},
			wantErrFrag: "non-user principal rejected",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user, err := validateKanopyJWT(tc.token(), testOperatorGroup, cache)
			if tc.wantErrFrag != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErrFrag)
				require.Nil(t, user)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, user)
			require.Equal(t, tc.wantRole, user.Role)
		})
	}
}

func TestValidateKanopyJWT_JWKSServerDown(t *testing.T) {
	// Start a server, immediately close it, then verify we get a fetch error
	// rather than a panic or successful auth.
	key := getTestKey(t)
	srv := makeJWKSServer(t, &key.PublicKey, testKid)
	cache := makeTestCache(srv)
	srv.Close() // close before the request

	token := signToken(t, key, testKid, validUserClaims())
	_, err := validateKanopyJWT(token, testOperatorGroup, cache)
	require.Error(t, err)
}

func TestJwkToRSA(t *testing.T) {
	key := getTestKey(t)
	pub := &key.PublicKey

	entry := jwkEntry{
		Kty: "RSA",
		Kid: "k1",
		N:   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
	got, err := jwkToRSA(entry)
	require.NoError(t, err)
	require.Equal(t, pub.N, got.N)
	require.Equal(t, pub.E, got.E)
}

func TestDevBypassUser(t *testing.T) {
	t.Run("inactive when env var not set", func(t *testing.T) {
		t.Setenv("DEV_BYPASS_AUTH", "0")
		require.Nil(t, devBypassUser())
	})

	t.Run("returns operator user when active", func(t *testing.T) {
		t.Setenv("DEV_BYPASS_AUTH", "1")
		t.Setenv("DEV_BYPASS_AUTH_EMAIL", "testuser@local.mongodb.com")
		u := devBypassUser()
		require.NotNil(t, u)
		require.Equal(t, RoleOperator, u.Role)
		require.Equal(t, "testuser", u.Login)
	})
}
