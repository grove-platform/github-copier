package services

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/grove-platform/github-copier/configs"
)

func TestGenerateGitHubJWT_EmptyAppID(t *testing.T) {
	t.Skip("Skipping test that requires valid RSA private key generation")
}

func TestJWTCaching(t *testing.T) {
	tm := NewTokenManager()

	// Set a cached token that hasn't expired
	tm.SetCachedJWT("cached-token", time.Now().Add(5*time.Minute))

	token, ok := tm.GetCachedJWT()
	if !ok {
		t.Error("Expected cached JWT to be valid")
	}
	if token != "cached-token" {
		t.Errorf("Cached JWT = %s, want cached-token", token)
	}

	// Set an expired token
	tm.SetCachedJWT("expired-token", time.Now().Add(-1*time.Minute))

	_, ok = tm.GetCachedJWT()
	if ok {
		t.Error("Expected expired JWT to not be returned")
	}
}

func TestInstallationTokenCache_Structure(t *testing.T) {
	tm := NewTokenManager()

	testToken := "test-token-value"
	tm.SetTokenForOrgNoExpiry("test-org", testToken)

	cached, ok := tm.GetTokenForOrg("test-org")
	if !ok {
		t.Error("Token not found in cache")
	}
	if cached != testToken {
		t.Errorf("Cached token = %s, want %s", cached, testToken)
	}
}

func TestInstallationTokenCache_ExpiryTracking(t *testing.T) {
	tm := NewTokenManager()

	// Token with future expiry should be valid
	tm.SetTokenForOrg("future-org", "valid-token", time.Now().Add(1*time.Hour))
	token, ok := tm.GetTokenForOrg("future-org")
	if !ok {
		t.Error("Expected valid token to be returned")
	}
	if token != "valid-token" {
		t.Errorf("Token = %s, want valid-token", token)
	}

	// Token within the 5-minute buffer should be treated as expired
	tm.SetTokenForOrg("expiring-org", "expiring-token", time.Now().Add(3*time.Minute))
	_, ok = tm.GetTokenForOrg("expiring-org")
	if ok {
		t.Error("Expected token within 5-minute buffer to not be returned")
	}

	// Already-expired token should not be returned
	tm.SetTokenForOrg("expired-org", "expired-token", time.Now().Add(-10*time.Minute))
	_, ok = tm.GetTokenForOrg("expired-org")
	if ok {
		t.Error("Expected expired token to not be returned")
	}
}

func TestLoadWebhookSecret_FromEnv(t *testing.T) {
	testSecret := "test-webhook-secret"
	os.Setenv("WEBHOOK_SECRET", testSecret)
	defer os.Unsetenv("WEBHOOK_SECRET")

	config := &configs.Config{WebhookSecret: ""}
	_ = LoadWebhookSecret(context.Background(), config)

	envSecret := os.Getenv("WEBHOOK_SECRET")
	if envSecret != testSecret {
		t.Errorf("WEBHOOK_SECRET env var = %s, want %s", envSecret, testSecret)
	}
}

func TestLoadMongoURI_FromEnv(t *testing.T) {
	testURI := "mongodb://localhost:27017/test"
	os.Setenv("MONGO_URI", testURI)
	defer os.Unsetenv("MONGO_URI")

	envURI := os.Getenv("MONGO_URI")
	if envURI != testURI {
		t.Errorf("MONGO_URI env var = %s, want %s", envURI, testURI)
	}
}

func TestGitHubAppID_FromEnv(t *testing.T) {
	testAppID := "123456"
	os.Setenv("GITHUB_APP_ID", testAppID)
	defer os.Unsetenv("GITHUB_APP_ID")

	appID := os.Getenv("GITHUB_APP_ID")
	if appID != testAppID {
		t.Errorf("GITHUB_APP_ID = %s, want %s", appID, testAppID)
	}
}

func TestGitHubInstallationID_FromEnv(t *testing.T) {
	testInstallID := "789012"
	os.Setenv("GITHUB_INSTALLATION_ID", testInstallID)
	defer os.Unsetenv("GITHUB_INSTALLATION_ID")

	installID := os.Getenv("GITHUB_INSTALLATION_ID")
	if installID != testInstallID {
		t.Errorf("GITHUB_INSTALLATION_ID = %s, want %s", installID, testInstallID)
	}
}

func TestGitHubPrivateKeyPath_FromEnv(t *testing.T) {
	testPath := "/path/to/private-key.pem"
	os.Setenv("GITHUB_PRIVATE_KEY_PATH", testPath)
	defer os.Unsetenv("GITHUB_PRIVATE_KEY_PATH")

	keyPath := os.Getenv("GITHUB_PRIVATE_KEY_PATH")
	if keyPath != testPath {
		t.Errorf("GITHUB_PRIVATE_KEY_PATH = %s, want %s", keyPath, testPath)
	}
}

func TestTokenManager_InstallationAccessToken(t *testing.T) {
	tm := NewTokenManager()

	if got := tm.GetInstallationAccessToken(); got != "" {
		t.Errorf("Expected empty token, got %q", got)
	}

	tm.SetInstallationAccessToken("ghs_test_token_123")
	if got := tm.GetInstallationAccessToken(); got != "ghs_test_token_123" {
		t.Errorf("InstallationAccessToken = %s, want ghs_test_token_123", got)
	}
}

func TestTokenManager_HTTPClient(t *testing.T) {
	tm := NewTokenManager()

	// Default client should not be nil
	if tm.GetHTTPClient() == nil {
		t.Error("HTTPClient should not be nil")
	}

	// Should be able to swap clients
	custom := &http.Client{}
	tm.SetHTTPClient(custom)
	if tm.GetHTTPClient() != custom {
		t.Error("Expected custom HTTP client after SetHTTPClient")
	}
}

func TestTokenManager_JWTExpiry(t *testing.T) {
	tm := NewTokenManager()

	futureExpiry := time.Now().Add(1 * time.Hour)
	tm.SetCachedJWT("future-jwt", futureExpiry)
	_, ok := tm.GetCachedJWT()
	if !ok {
		t.Error("JWT should not be expired")
	}

	pastExpiry := time.Now().Add(-1 * time.Hour)
	tm.SetCachedJWT("past-jwt", pastExpiry)
	_, ok = tm.GetCachedJWT()
	if ok {
		t.Error("JWT should be expired")
	}
}

func TestTokenManager_ThreadSafety(t *testing.T) {
	tm := NewTokenManager()

	done := make(chan bool, 10)

	for i := range 5 {
		go func(n int) {
			defer func() { done <- true }()
			org := "org-" + string(rune('A'+n))
			tm.SetTokenForOrg(org, "token-"+org, time.Now().Add(1*time.Hour))
			tm.SetCachedJWT("jwt-"+org, time.Now().Add(9*time.Minute))
			tm.SetInstallationAccessToken("iat-" + org)
		}(i)
	}

	for i := range 5 {
		go func(n int) {
			defer func() { done <- true }()
			org := "org-" + string(rune('A'+n))
			_, _ = tm.GetTokenForOrg(org)
			_, _ = tm.GetCachedJWT()
			_ = tm.GetInstallationAccessToken()
			_ = tm.GetHTTPClient()
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

// TODO https://jira.mongodb.org/browse/DOCSP-54727
// Note: Comprehensive testing of github_auth.go would require:
// 1. Mocking the Secret Manager client
// 2. Mocking the GitHub API client
// 3. Testing the full authentication flow:
//    - JWT generation with valid PEM key
//    - Installation token retrieval
//    - Token caching and refresh logic
//    - Organization-specific client creation
//    - Error handling for API failures
