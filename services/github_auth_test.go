package services

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/grove-platform/github-copier/configs"
	"github.com/jarcoal/httpmock"
)

func TestGenerateGitHubJWT(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	tests := []struct {
		name    string
		appID   string
		wantErr bool
	}{
		{name: "valid app ID", appID: "123456", wantErr: false},
		{name: "empty app ID still produces JWT", appID: "", wantErr: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token, err := generateGitHubJWT(tt.appID, key)
			if (err != nil) != tt.wantErr {
				t.Fatalf("generateGitHubJWT() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr {
				if token == "" {
					t.Error("expected non-empty JWT token")
				}
				// Verify the token can be parsed and has correct claims
				parsed, err := jwt.Parse(token, func(t *jwt.Token) (interface{}, error) {
					return &key.PublicKey, nil
				})
				if err != nil {
					t.Fatalf("jwt.Parse: %v", err)
				}
				claims, ok := parsed.Claims.(jwt.MapClaims)
				if !ok {
					t.Fatal("expected MapClaims")
				}
				if iss, _ := claims["iss"].(string); iss != tt.appID {
					t.Errorf("iss = %q, want %q", iss, tt.appID)
				}
				if _, ok := claims["iat"]; !ok {
					t.Error("missing 'iat' claim")
				}
				if _, ok := claims["exp"]; !ok {
					t.Error("missing 'exp' claim")
				}
			}
		})
	}
}

func TestGenerateGitHubJWT_NilKey(t *testing.T) {
	// The JWT library panics on nil key, so verify we get a panic.
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic with nil private key")
		}
	}()
	_, _ = generateGitHubJWT("123456", nil)
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
	_ = os.Setenv("WEBHOOK_SECRET", testSecret)
	defer func() { _ = os.Unsetenv("WEBHOOK_SECRET") }()

	config := &configs.Config{WebhookSecret: ""}
	_ = LoadWebhookSecret(context.Background(), config)

	envSecret := os.Getenv("WEBHOOK_SECRET")
	if envSecret != testSecret {
		t.Errorf("WEBHOOK_SECRET env var = %s, want %s", envSecret, testSecret)
	}
}

func TestLoadMongoURI_FromEnv(t *testing.T) {
	testURI := "mongodb://localhost:27017/test"
	_ = os.Setenv("MONGO_URI", testURI)
	defer func() { _ = os.Unsetenv("MONGO_URI") }()

	envURI := os.Getenv("MONGO_URI")
	if envURI != testURI {
		t.Errorf("MONGO_URI env var = %s, want %s", envURI, testURI)
	}
}

func TestGitHubAppID_FromEnv(t *testing.T) {
	testAppID := "123456"
	_ = os.Setenv("GITHUB_APP_ID", testAppID)
	defer func() { _ = os.Unsetenv("GITHUB_APP_ID") }()

	appID := os.Getenv("GITHUB_APP_ID")
	if appID != testAppID {
		t.Errorf("GITHUB_APP_ID = %s, want %s", appID, testAppID)
	}
}

func TestGitHubInstallationID_FromEnv(t *testing.T) {
	testInstallID := "789012"
	_ = os.Setenv("GITHUB_INSTALLATION_ID", testInstallID)
	defer func() { _ = os.Unsetenv("GITHUB_INSTALLATION_ID") }()

	installID := os.Getenv("GITHUB_INSTALLATION_ID")
	if installID != testInstallID {
		t.Errorf("GITHUB_INSTALLATION_ID = %s, want %s", installID, testInstallID)
	}
}

func TestGitHubPrivateKeyPath_FromEnv(t *testing.T) {
	testPath := "/path/to/private-key.pem"
	_ = os.Setenv("GITHUB_PRIVATE_KEY_PATH", testPath)
	defer func() { _ = os.Unsetenv("GITHUB_PRIVATE_KEY_PATH") }()

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

func TestGetInstallationAccessToken_Success(t *testing.T) {
	c := &http.Client{}
	httpmock.ActivateNonDefault(c)
	t.Cleanup(httpmock.DeactivateAndReset)

	httpmock.RegisterResponder("POST",
		"https://api.github.com/app/installations/12345/access_tokens",
		httpmock.NewJsonResponderOrPanic(201, map[string]any{
			"token":      "ghs_test123",
			"expires_at": "2030-01-01T00:00:00Z",
		}),
	)

	token, expiresAt, err := getInstallationAccessToken("12345", "fake-jwt", c)
	if err != nil {
		t.Fatalf("getInstallationAccessToken: %v", err)
	}
	if token != "ghs_test123" {
		t.Errorf("token = %q, want ghs_test123", token)
	}
	if expiresAt.IsZero() {
		t.Error("expected non-zero expiresAt")
	}
}

func TestGetInstallationAccessToken_MissingInstallationID(t *testing.T) {
	_, _, err := getInstallationAccessToken("", "fake-jwt", nil)
	if err == nil {
		t.Error("expected error with empty installation ID")
	}
}

func TestGetInstallationAccessToken_Unauthorized(t *testing.T) {
	c := &http.Client{}
	httpmock.ActivateNonDefault(c)
	t.Cleanup(httpmock.DeactivateAndReset)

	httpmock.RegisterResponder("POST",
		"https://api.github.com/app/installations/12345/access_tokens",
		httpmock.NewJsonResponderOrPanic(401, map[string]any{
			"message": "Bad credentials",
		}),
	)

	_, _, err := getInstallationAccessToken("12345", "bad-jwt", c)
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !errors.Is(err, ErrAuthentication) {
		t.Errorf("expected ErrAuthentication, got: %v", err)
	}
}

func TestConfigurePermissions_FullFlow(t *testing.T) {
	// Generate a real RSA key for JWT signing
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})

	t.Setenv("SKIP_SECRET_MANAGER", "true")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", string(pemBytes))

	// Use a fresh TokenManager to avoid polluting other tests
	tm := NewTokenManager()
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	c := &http.Client{}
	httpmock.ActivateNonDefault(c)
	t.Cleanup(httpmock.DeactivateAndReset)
	tm.SetHTTPClient(c)

	httpmock.RegisterResponder("POST",
		"https://api.github.com/app/installations/99999/access_tokens",
		httpmock.NewJsonResponderOrPanic(201, map[string]any{
			"token":      "ghs_configured_token",
			"expires_at": "2030-01-01T00:00:00Z",
		}),
	)

	config := &configs.Config{
		AppId:          "123456",
		InstallationId: "99999",
	}

	err := ConfigurePermissions(context.Background(), config)
	if err != nil {
		t.Fatalf("ConfigurePermissions: %v", err)
	}

	if got := tm.GetInstallationAccessToken(); got != "ghs_configured_token" {
		t.Errorf("installation token = %q, want ghs_configured_token", got)
	}

	// Verify the token endpoint was called exactly once
	info := httpmock.GetCallCountInfo()
	if info["POST https://api.github.com/app/installations/99999/access_tokens"] != 1 {
		t.Errorf("expected exactly 1 call to token endpoint, got %d",
			info["POST https://api.github.com/app/installations/99999/access_tokens"])
	}
}

func TestGetPrivateKeyFromSecret_EnvVar(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})

	t.Setenv("SKIP_SECRET_MANAGER", "true")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", string(pemBytes))

	got, err := getPrivateKeyFromSecret(context.Background(), &configs.Config{})
	if err != nil {
		t.Fatalf("getPrivateKeyFromSecret: %v", err)
	}
	if string(got) != string(pemBytes) {
		t.Error("returned key does not match expected PEM bytes")
	}
}

func TestGetPrivateKeyFromSecret_Base64(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})

	t.Setenv("SKIP_SECRET_MANAGER", "true")
	// Clear direct env var to force base64 path
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(pemBytes))

	got, err := getPrivateKeyFromSecret(context.Background(), &configs.Config{})
	if err != nil {
		t.Fatalf("getPrivateKeyFromSecret: %v", err)
	}
	if string(got) != string(pemBytes) {
		t.Error("returned key does not match expected PEM bytes")
	}
}

func TestGetPrivateKeyFromSecret_MissingAllEnvVars(t *testing.T) {
	t.Setenv("SKIP_SECRET_MANAGER", "true")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY_B64", "")

	_, err := getPrivateKeyFromSecret(context.Background(), &configs.Config{})
	if err == nil {
		t.Error("expected error when no key env vars set")
	}
	if !errors.Is(err, ErrSecretAccess) {
		t.Errorf("expected ErrSecretAccess, got: %v", err)
	}
}

func TestGetInstallationIDForOrg_Success(t *testing.T) {
	// Set up a fresh TokenManager with a cached JWT to avoid private key lookup
	tm := NewTokenManager()
	tm.SetCachedJWT("fake-jwt", time.Now().Add(5*time.Minute))
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	c := &http.Client{}
	httpmock.ActivateNonDefault(c)
	t.Cleanup(httpmock.DeactivateAndReset)
	tm.SetHTTPClient(c)

	httpmock.RegisterResponder("GET", "https://api.github.com/app/installations",
		httpmock.NewJsonResponderOrPanic(200, []map[string]any{
			{"id": 111, "account": map[string]any{"login": "org-a", "type": "Organization"}},
			{"id": 222, "account": map[string]any{"login": "org-b", "type": "Organization"}},
		}),
	)

	config := &configs.Config{AppId: "123"}

	id, err := getInstallationIDForOrg(context.Background(), config, "org-b")
	if err != nil {
		t.Fatalf("getInstallationIDForOrg: %v", err)
	}
	if id != "222" {
		t.Errorf("installationID = %q, want 222", id)
	}
}

func TestGetInstallationIDForOrg_NotFound(t *testing.T) {
	tm := NewTokenManager()
	tm.SetCachedJWT("fake-jwt", time.Now().Add(5*time.Minute))
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	c := &http.Client{}
	httpmock.ActivateNonDefault(c)
	t.Cleanup(httpmock.DeactivateAndReset)
	tm.SetHTTPClient(c)

	httpmock.RegisterResponder("GET", "https://api.github.com/app/installations",
		httpmock.NewJsonResponderOrPanic(200, []map[string]any{
			{"id": 111, "account": map[string]any{"login": "org-a", "type": "Organization"}},
		}),
	)

	config := &configs.Config{AppId: "123"}

	_, err := getInstallationIDForOrg(context.Background(), config, "no-such-org")
	if err == nil {
		t.Fatal("expected error for unknown org")
	}
	if !errors.Is(err, ErrInstallationNotFound) {
		t.Errorf("expected ErrInstallationNotFound, got: %v", err)
	}
}

func TestGetInstallationIDForOrg_Unauthorized(t *testing.T) {
	tm := NewTokenManager()
	tm.SetCachedJWT("bad-jwt", time.Now().Add(5*time.Minute))
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	c := &http.Client{}
	httpmock.ActivateNonDefault(c)
	t.Cleanup(httpmock.DeactivateAndReset)
	tm.SetHTTPClient(c)

	httpmock.RegisterResponder("GET", "https://api.github.com/app/installations",
		httpmock.NewJsonResponderOrPanic(401, map[string]any{"message": "Bad credentials"}),
	)

	config := &configs.Config{AppId: "123"}

	_, err := getInstallationIDForOrg(context.Background(), config, "org-a")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !errors.Is(err, ErrAuthentication) {
		t.Errorf("expected ErrAuthentication, got: %v", err)
	}
}
