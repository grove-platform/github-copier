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
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/sync/singleflight"
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
	// Scp carries scopes for service-principal and mesh-internal tokens.
	// Human user tokens do not set this field.
	Scp []string `json:"scp"`
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
// endpoint. Construct with newKanopyJWKSCache; the zero value is not valid.
//
// Concurrency: singleflight deduplicates concurrent fetches so a slow JWKS
// endpoint degrades individual request latency (singleflight wait) rather
// than serialising all auth requests behind a write lock for the full HTTP
// timeout. The write lock is held only for the in-memory cache update, not
// across the network call.
type kanopyJWKSCache struct {
	url     string
	mu      sync.RWMutex
	keys    map[string]*rsa.PublicKey // kid → public key
	fetched time.Time
	failed  time.Time
	sf      singleflight.Group
}

func newKanopyJWKSCache(url string) *kanopyJWKSCache {
	if url == "" {
		url = kanopyJWKSURLDefault
	}
	return &kanopyJWKSCache{url: url}
}

func (c *kanopyJWKSCache) getKeys() (map[string]*rsa.PublicKey, error) {
	// Fast path: cache is fresh.
	c.mu.RLock()
	if !c.fetched.IsZero() && time.Since(c.fetched) < jwksTTL {
		keys := c.keys
		c.mu.RUnlock()
		return keys, nil
	}
	// Failure cooldown: serve stale keys rather than hammering a downed endpoint.
	if !c.failed.IsZero() && time.Since(c.failed) < jwksFailureCooldown {
		keys := c.keys
		c.mu.RUnlock()
		if keys != nil {
			return keys, nil
		}
		return nil, fmt.Errorf("JWKS unavailable: in backoff after failed fetch")
	}
	c.mu.RUnlock()

	// Slow path: deduplicate concurrent fetches with singleflight.
	type result struct {
		keys map[string]*rsa.PublicKey
		err  error
	}
	v, _, _ := c.sf.Do("fetch", func() (any, error) {
		// Re-check after winning the singleflight slot — another goroutine may
		// have refreshed the cache while we were waiting.
		c.mu.RLock()
		if !c.fetched.IsZero() && time.Since(c.fetched) < jwksTTL {
			k := c.keys
			c.mu.RUnlock()
			return result{keys: k}, nil
		}
		c.mu.RUnlock()

		// Fetch with background context — JWKS is a shared resource not tied to
		// any individual request. Using the caller's context would cancel the
		// fetch (and invalidate it for all singleflight waiters) if that
		// specific request times out first.
		keys, fetchErr := fetchAndParseJWKS(context.Background(), c.url)

		c.mu.Lock()
		defer c.mu.Unlock()
		if fetchErr != nil {
			c.failed = time.Now()
			if c.keys != nil {
				return result{keys: c.keys}, nil // serve stale on transient failure
			}
			return result{err: fmt.Errorf("JWKS fetch failed: %w", fetchErr)}, nil
		}
		c.keys = keys
		c.fetched = time.Now()
		c.failed = time.Time{}
		return result{keys: keys}, nil
	})
	r := v.(result)
	return r.keys, r.err
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
// authenticated human users receive RoleWriter.
//
// cache is the caller-owned JWKS cache — injected rather than global so tests
// can point at an httptest server without process-wide side effects.
func validateKanopyJWT(rawToken string, operatorGroup string, cache *kanopyJWKSCache) (*OperatorUser, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("empty token")
	}

	var claims corpSecureClaims
	_, err := jwt.ParseWithClaims(rawToken, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		keys, err := cache.getKeys()
		if err != nil {
			return nil, err
		}
		kid, _ := t.Header["kid"].(string)
		key, ok := keys[kid]
		if !ok {
			// Do not fall back to an arbitrary key — random map iteration would
			// pick the wrong key during rotation and produce spurious rejections.
			return nil, fmt.Errorf("no key for kid %q in JWKS (%d keys cached)", kid, len(keys))
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

	// Reject service-mesh principals. Per corpsecure/mesh.md and the
	// 2025-01-31_mesh_service_principal_identity advisory, mesh-internal tokens
	// are minted by CorpSecure for all pod-to-pod requests when mesh.enabled=true.
	// They carry scp=["mesh-internal"] and a spiffe:// subject instead of an
	// email + groups. They pass signature and issuer checks identically to user
	// tokens — the operator UI must explicitly reject them to prevent any
	// mesh-resident workload from reading audit events, delivery logs, and
	// workflow config.
	if claims.Email == "" ||
		slices.Contains(claims.Scp, "mesh-internal") ||
		strings.HasPrefix(claims.Subject, "spiffe://") {
		return nil, fmt.Errorf("non-user principal rejected (service-mesh identity)")
	}

	role := RoleWriter
	if slices.Contains(claims.Groups, operatorGroup) {
		role = RoleOperator
	}

	return &OperatorUser{
		Login: claims.Subject,
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
