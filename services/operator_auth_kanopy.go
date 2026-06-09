package services

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// corpSecureClaims represents the JWT claims forwarded by Kanopy's CorpSecure
// proxy on the X-Kanopy-Internal-Authorization header.
//
// No `aud` claim is present — per Kanopy's CorpSecure docs, audience checks
// must be disabled. See agent-skill-dashboard lib/auth.ts for precedent.
type corpSecureClaims struct {
	jwt.RegisteredClaims
	Email  string   `json:"email"`
	Groups []string `json:"groups"`
}

// kanopyJWKSURLDefault is the prod JWKS endpoint. Override with
// OPERATOR_AUTH_KANOPY_JWKS_URL for the staging endpoint
// (https://login.staging.corp.mongodb.com/.well-known/jwks.json).
// The issuer claim is login.corp.mongodb.com in both environments.
const kanopyJWKSURLDefault = "https://login.corp.mongodb.com/.well-known/jwks.json"
const kanopyIssuer = "login.corp.mongodb.com"

const jwksTTL = 10 * time.Minute
const jwksFailureCooldown = 30 * time.Second

// kanopyJWKSCache fetches and caches RSA public keys from the CorpSecure JWKS
// endpoint. A single global instance is used per process.
//
// Concurrency model: callers acquire a read lock to check freshness; on a miss
// they upgrade to a write lock and fetch. The double-checked locking pattern
// ensures at most one in-flight fetch at a time. Holding the write lock during
// the HTTP call is intentional — the fetch takes ~100ms and occurs at most
// once per jwksTTL (10 min). Serving slightly stale keys under brief lock
// contention is acceptable.
type kanopyJWKSCache struct {
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey // kid → public key
	fetched time.Time
	failed  time.Time
	url     string
}

var globalKanopyJWKS = &kanopyJWKSCache{}

func (c *kanopyJWKSCache) getKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	c.mu.RLock()
	if !c.fetched.IsZero() && time.Since(c.fetched) < jwksTTL {
		keys := c.keys
		c.mu.RUnlock()
		return keys, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock.
	if !c.fetched.IsZero() && time.Since(c.fetched) < jwksTTL {
		return c.keys, nil
	}
	// Failure cooldown: serve stale keys if available rather than hammering
	// a downed JWKS endpoint on every request.
	if !c.failed.IsZero() && time.Since(c.failed) < jwksFailureCooldown {
		if c.keys != nil {
			return c.keys, nil
		}
		return nil, fmt.Errorf("JWKS unavailable: in backoff after failed fetch")
	}

	endpoint := c.url
	if endpoint == "" {
		endpoint = kanopyJWKSURLDefault
	}
	keys, err := fetchAndParseJWKS(ctx, endpoint)
	if err != nil {
		c.failed = time.Now()
		if c.keys != nil {
			return c.keys, nil // serve last-known-good on transient failure
		}
		return nil, fmt.Errorf("JWKS fetch failed: %w", err)
	}
	c.keys = keys
	c.fetched = time.Now()
	c.failed = time.Time{}
	return keys, nil
}

// jwkEntry is the minimal subset of a JWK we need.
type jwkEntry struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwkSet struct {
	Keys []jwkEntry `json:"keys"`
}

func fetchAndParseJWKS(ctx context.Context, url string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // #nosec G107 -- URL is from config, not user input
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<17)) // 128 KB cap
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("JWKS HTTP %d", resp.StatusCode)
	}
	if readErr != nil {
		return nil, fmt.Errorf("read JWKS response: %w", readErr)
	}

	var set jwkSet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("parse JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || k.N == "" || k.E == "" {
			continue
		}
		pub, err := jwkToRSA(k)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no valid RSA keys in JWKS response")
	}
	return keys, nil
}

func jwkToRSA(k jwkEntry) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode e: %w", err)
	}
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	if e == 0 {
		return nil, fmt.Errorf("invalid exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

// validateKanopyJWT verifies a CorpSecure JWT and returns an OperatorUser.
// operatorGroup is the Okta group whose members receive RoleOperator; all other
// authenticated employees receive RoleWriter.
func validateKanopyJWT(ctx context.Context, rawToken string, operatorGroup string, jwksURL string) (*OperatorUser, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("empty token")
	}

	cache := globalKanopyJWKS
	cache.mu.Lock()
	if cache.url == "" && jwksURL != "" {
		cache.url = jwksURL
	}
	cache.mu.Unlock()

	var claims corpSecureClaims
	_, err := jwt.ParseWithClaims(rawToken, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		keys, err := cache.getKeys(ctx)
		if err != nil {
			return nil, err
		}
		kid, _ := t.Header["kid"].(string)
		key, ok := keys[kid]
		if !ok {
			// kid may be empty or rotate; try any key as fallback
			for _, k := range keys {
				return k, nil
			}
			return nil, fmt.Errorf("no matching key for kid %q", kid)
		}
		return key, nil
	},
		jwt.WithIssuer(kanopyIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
		// No WithAudience — CorpSecure JWTs do not carry an aud claim by design.
	)
	if err != nil {
		return nil, fmt.Errorf("verify token: %w", err)
	}

	login := claims.Subject
	if login == "" {
		return nil, fmt.Errorf("empty sub claim")
	}

	role := RoleWriter
	if slices.Contains(claims.Groups, operatorGroup) {
		role = RoleOperator
	}

	return &OperatorUser{
		Login: login,
		Role:  role,
	}, nil
}

// init runs the dev-bypass safety check once at process startup — not on the
// first request — so a misconfigured pod is refused before it ever serves
// traffic. Mirrors agent-skill-dashboard lib/auth.ts (assertDevBypassIsLocalDev).
func init() {
	if os.Getenv("DEV_BYPASS_AUTH") != "1" {
		return
	}
	inCluster := os.Getenv("KUBERNETES_SERVICE_HOST") != "" || os.Getenv("KANOPY_NAMESPACE") != ""
	if inCluster {
		log.Fatal("[kanopy auth] DEV_BYPASS_AUTH=1 is set inside a Kubernetes pod — refusing to start. Unset it from the deployment config.")
	}
}

// devBypassUser returns a synthetic OperatorUser when DEV_BYPASS_AUTH=1.
// The Kubernetes safety check is enforced at startup by init(); this function
// is a non-panicking helper that callers invoke per-request.
// Returns nil when the bypass is not active.
func devBypassUser() *OperatorUser {
	if os.Getenv("DEV_BYPASS_AUTH") != "1" {
		return nil
	}
	email := os.Getenv("DEV_BYPASS_AUTH_EMAIL")
	if email == "" {
		email = "dev-user@local.mongodb.com"
	}
	login := email
	if idx := len(email) - len("@local.mongodb.com"); idx > 0 && email[idx:] == "@local.mongodb.com" {
		login = email[:idx]
	}
	return &OperatorUser{Login: login, Role: RoleOperator}
}
