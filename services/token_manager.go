package services

import (
	"net/http"
	"sync"
	"time"
)

// tokenEntry stores a cached installation token with its expiration time.
type tokenEntry struct {
	Token     string
	ExpiresAt time.Time
}

// TokenManager provides thread-safe management of GitHub App authentication tokens.
// It caches JWT tokens and per-org installation tokens with expiry tracking,
// and holds the HTTP client used for GitHub API calls.
type TokenManager struct {
	mu sync.RWMutex

	// Default installation access token (set once at startup via ConfigurePermissions)
	installationAccessToken string

	// Per-org installation token cache with expiry
	installationTokenCache map[string]tokenEntry

	// Cached JWT token and its expiry
	cachedJWT       string
	cachedJWTExpiry time.Time

	// HTTP client used for GitHub API calls (swappable for testing with httpmock)
	httpClient *http.Client
}

// NewTokenManager creates a new TokenManager instance.
func NewTokenManager() *TokenManager {
	return &TokenManager{
		installationTokenCache: make(map[string]tokenEntry),
		httpClient:             http.DefaultClient,
	}
}

// GetInstallationAccessToken returns the default installation access token.
func (tm *TokenManager) GetInstallationAccessToken() string {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return tm.installationAccessToken
}

// SetInstallationAccessToken sets the default installation access token.
func (tm *TokenManager) SetInstallationAccessToken(token string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.installationAccessToken = token
}

// GetHTTPClient returns the HTTP client used for GitHub API calls.
func (tm *TokenManager) GetHTTPClient() *http.Client {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	if tm.httpClient == nil {
		return http.DefaultClient
	}
	return tm.httpClient
}

// SetHTTPClient sets the HTTP client used for GitHub API calls.
func (tm *TokenManager) SetHTTPClient(client *http.Client) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.httpClient = client
}

// GetTokenForOrg returns a cached token for the given org if it exists and is still valid.
// Returns empty string and false if no valid token exists.
func (tm *TokenManager) GetTokenForOrg(org string) (string, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	entry, ok := tm.installationTokenCache[org]
	if !ok || entry.Token == "" {
		return "", false
	}
	// Check if token is expired (with 5-minute buffer for safety)
	if !entry.ExpiresAt.IsZero() && time.Now().After(entry.ExpiresAt.Add(-5*time.Minute)) {
		return "", false
	}
	return entry.Token, true
}

// SetTokenForOrg caches an installation token for an org with expiry.
func (tm *TokenManager) SetTokenForOrg(org, token string, expiresAt time.Time) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.installationTokenCache[org] = tokenEntry{
		Token:     token,
		ExpiresAt: expiresAt,
	}
}

// SetTokenForOrgNoExpiry caches an installation token for an org without expiry tracking.
// Primarily used in tests where token expiry is not relevant.
func (tm *TokenManager) SetTokenForOrgNoExpiry(org, token string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.installationTokenCache[org] = tokenEntry{
		Token: token,
	}
}

// GetCachedJWT returns the cached JWT if it's still valid.
func (tm *TokenManager) GetCachedJWT() (string, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	if tm.cachedJWT != "" && time.Now().Before(tm.cachedJWTExpiry) {
		return tm.cachedJWT, true
	}
	return "", false
}

// SetCachedJWT caches a JWT with its expiry time.
func (tm *TokenManager) SetCachedJWT(token string, expiry time.Time) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.cachedJWT = token
	tm.cachedJWTExpiry = expiry
}

// defaultTokenManager is the package-level TokenManager instance.
var defaultTokenManager = NewTokenManager()

// DefaultTokenManager returns the package-level TokenManager instance.
func DefaultTokenManager() *TokenManager {
	return defaultTokenManager
}
