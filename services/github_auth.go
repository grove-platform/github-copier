package services

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v48/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/shurcooL/graphql"
	"golang.org/x/oauth2"
)

// transport is a custom HTTP transport that adds the Authorization header to each request.
type transport struct {
	token string
}

// ConfigurePermissions sets up the necessary permissions to interact with the GitHub API.
// It retrieves the GitHub App's private key from Google Secret Manager, generates a JWT,
// and exchanges it for an installation access token stored in the TokenManager.
func ConfigurePermissions(ctx context.Context, config *configs.Config) error {
	pemKey, err := getPrivateKeyFromSecret(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to get private key: %w", err)
	}

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(pemKey)
	if err != nil {
		return fmt.Errorf("unable to parse RSA private key: %w", err)
	}

	// Generate JWT — use the numeric GitHub App ID (GITHUB_APP_ID) as "iss"
	token, err := generateGitHubJWT(config.AppId, privateKey)
	if err != nil {
		return fmt.Errorf("error generating JWT: %w", err)
	}

	hc := defaultTokenManager.GetHTTPClient()
	installationToken, _, err := getInstallationAccessToken(config.InstallationId, token, hc)
	if err != nil {
		return fmt.Errorf("error getting installation access token: %w", err)
	}
	defaultTokenManager.SetInstallationAccessToken(installationToken)
	return nil
}

// generateGitHubJWT creates a JWT for GitHub App authentication.
func generateGitHubJWT(appID string, privateKey *rsa.PrivateKey) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(time.Minute * 10).Unix(),
		"iss": appID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signedToken, err := token.SignedString(privateKey)
	if err != nil {
		return "", fmt.Errorf("unable to sign JWT: %v", err)
	}
	return signedToken, nil
}

// getPrivateKeyFromSecret retrieves the GitHub App's private key from Google Secret Manager.
// It supports local testing by allowing the key to be provided via environment variables.
func getPrivateKeyFromSecret(ctx context.Context, config *configs.Config) ([]byte, error) {
	if os.Getenv("SKIP_SECRET_MANAGER") == "true" {
		if pem := os.Getenv("GITHUB_APP_PRIVATE_KEY"); pem != "" {
			return []byte(pem), nil
		}
		if b64 := os.Getenv("GITHUB_APP_PRIVATE_KEY_B64"); b64 != "" {
			dec, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return nil, fmt.Errorf("invalid base64 private key: %w", err)
			}
			return dec, nil
		}
		return nil, fmt.Errorf("%w: SKIP_SECRET_MANAGER=true but no GITHUB_APP_PRIVATE_KEY or GITHUB_APP_PRIVATE_KEY_B64 set", ErrSecretAccess)
	}
	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to create Secret Manager client: %v", ErrSecretAccess, err)
	}
	defer client.Close()

	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: config.SecretPath(config.PEMKeyName),
	}
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSecretAccess, err)
	}
	return result.Payload.Data, nil
}

// getWebhookSecretFromSecretManager retrieves the webhook secret from Google Cloud Secret Manager
func getWebhookSecretFromSecretManager(ctx context.Context, secretName string) (string, error) {
	if os.Getenv("SKIP_SECRET_MANAGER") == "true" {
		if secret := os.Getenv(configs.WebhookSecret); secret != "" {
			return secret, nil
		}
		return "", fmt.Errorf("%w: SKIP_SECRET_MANAGER=true but no WEBHOOK_SECRET set", ErrSecretAccess)
	}

	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: failed to create Secret Manager client: %v", ErrSecretAccess, err)
	}
	defer client.Close()

	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretName,
	}
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSecretAccess, err)
	}
	return string(result.Payload.Data), nil
}

// LoadWebhookSecret loads the webhook secret from Secret Manager or environment variable
func LoadWebhookSecret(ctx context.Context, config *configs.Config) error {
	if config.WebhookSecret != "" {
		return nil
	}
	resolvedName := config.SecretPath(config.WebhookSecretName)
	secret, err := getWebhookSecretFromSecretManager(ctx, resolvedName)
	if err != nil {
		return fmt.Errorf("failed to load webhook secret: %w", err)
	}
	config.WebhookSecret = secret
	return nil
}

// LoadMongoURI loads the MongoDB URI from Secret Manager or environment variable
func LoadMongoURI(ctx context.Context, config *configs.Config) error {
	if config.MongoURI != "" {
		return nil
	}
	if config.MongoURISecretName == "" {
		return nil
	}
	resolvedName := config.SecretPath(config.MongoURISecretName)
	uri, err := getSecretFromSecretManager(ctx, resolvedName, "MONGO_URI")
	if err != nil {
		return fmt.Errorf("failed to load MongoDB URI: %w", err)
	}
	config.MongoURI = uri
	return nil
}

// getSecretFromSecretManager is a generic function to retrieve any secret from Secret Manager
func getSecretFromSecretManager(ctx context.Context, secretName, envVarName string) (string, error) {
	if os.Getenv("SKIP_SECRET_MANAGER") == "true" {
		if secret := os.Getenv(envVarName); secret != "" {
			return secret, nil
		}
		return "", fmt.Errorf("%w: SKIP_SECRET_MANAGER=true but no %s set", ErrSecretAccess, envVarName)
	}

	client, err := secretmanager.NewClient(ctx)
	if err != nil {
		return "", fmt.Errorf("%w: failed to create Secret Manager client: %v", ErrSecretAccess, err)
	}
	defer client.Close()

	req := &secretmanagerpb.AccessSecretVersionRequest{
		Name: secretName,
	}
	result, err := client.AccessSecretVersion(ctx, req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrSecretAccess, err)
	}
	return string(result.Payload.Data), nil
}

// getInstallationAccessToken exchanges a JWT for a GitHub App installation access token.
// Returns the token, its expiry time, and any error.
func getInstallationAccessToken(installationId, jwtTokenStr string, hc *http.Client) (string, time.Time, error) {
	if installationId == "" {
		return "", time.Time{}, fmt.Errorf("missing installation ID")
	}

	url := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", installationId)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwtTokenStr)
	req.Header.Set("Accept", "application/vnd.github+json")

	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			b = []byte(fmt.Sprintf("<failed to read body: %v>", readErr))
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return "", time.Time{}, fmt.Errorf("%w: failed to get installation access token (401). The GitHub App private key (PEM) may be invalid or expired. Please check the CODE_COPIER_PEM secret in GCP Secret Manager. Response: %s", ErrAuthentication, string(b))
		}
		return "", time.Time{}, fmt.Errorf("%w: status %d: %s", ErrAuthentication, resp.StatusCode, string(b))
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", time.Time{}, fmt.Errorf("decode: %w", err)
	}
	return out.Token, out.ExpiresAt, nil
}

// GetRestClient returns a GitHub REST API client authenticated with the default installation access token.
func GetRestClient() *github.Client {
	tm := defaultTokenManager
	token := tm.GetInstallationAccessToken()
	hc := tm.GetHTTPClient()

	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	base := http.DefaultTransport
	if hc != nil && hc.Transport != nil {
		base = hc.Transport
	}

	httpClient := &http.Client{
		Transport: &oauth2.Transport{
			Source: src,
			Base:   base,
		},
	}
	return github.NewClient(httpClient)
}

// GetGraphQLClient returns a GitHub GraphQL API client authenticated with the default installation access token.
func GetGraphQLClient(ctx context.Context, config *configs.Config) (*graphql.Client, error) {
	if defaultTokenManager.GetInstallationAccessToken() == "" {
		if err := ConfigurePermissions(ctx, config); err != nil {
			return nil, fmt.Errorf("failed to configure permissions: %w", err)
		}
	}
	client := graphql.NewClient("https://api.github.com/graphql", &http.Client{
		Transport: &transport{token: defaultTokenManager.GetInstallationAccessToken()},
	})
	return client, nil
}

// GetGraphQLClientForOrg returns a GitHub GraphQL API client authenticated for a specific organization.
// Uses the TokenManager for thread-safe token caching with expiry tracking.
func GetGraphQLClientForOrg(ctx context.Context, config *configs.Config, org string) (*graphql.Client, error) {
	if token, ok := defaultTokenManager.GetTokenForOrg(org); ok {
		client := graphql.NewClient("https://api.github.com/graphql", &http.Client{
			Transport: &transport{token: token},
		})
		return client, nil
	}

	installationID, err := getInstallationIDForOrg(ctx, config, org)
	if err != nil {
		return nil, fmt.Errorf("failed to get installation ID for org %s: %w", org, err)
	}

	token, err := getOrRefreshJWT(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to get JWT: %w", err)
	}

	hc := defaultTokenManager.GetHTTPClient()
	installationToken, expiresAt, err := getInstallationAccessToken(installationID, token, hc)
	if err != nil {
		return nil, fmt.Errorf("failed to get installation token for org %s: %w", org, err)
	}

	defaultTokenManager.SetTokenForOrg(org, installationToken, expiresAt)

	client := graphql.NewClient("https://api.github.com/graphql", &http.Client{
		Transport: &transport{token: installationToken},
	})
	return client, nil
}

// getOrRefreshJWT returns a valid JWT token, generating a new one if expired.
func getOrRefreshJWT(ctx context.Context, config *configs.Config) (string, error) {
	if cachedToken, ok := defaultTokenManager.GetCachedJWT(); ok {
		return cachedToken, nil
	}

	pemKey, err := getPrivateKeyFromSecret(ctx, config)
	if err != nil {
		return "", fmt.Errorf("failed to get private key: %w", err)
	}

	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(pemKey)
	if err != nil {
		return "", fmt.Errorf("unable to parse RSA private key: %w", err)
	}

	token, err := generateGitHubJWT(config.AppId, privateKey)
	if err != nil {
		return "", fmt.Errorf("error generating JWT: %w", err)
	}

	defaultTokenManager.SetCachedJWT(token, time.Now().Add(9*time.Minute))
	return token, nil
}

// getInstallationIDForOrg retrieves the installation ID for a specific organization
func getInstallationIDForOrg(ctx context.Context, config *configs.Config, org string) (string, error) {
	token, err := getOrRefreshJWT(ctx, config)
	if err != nil {
		return "", fmt.Errorf("failed to get JWT: %w", err)
	}

	url := "https://api.github.com/app/installations"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	hc := defaultTokenManager.GetHTTPClient()
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			body = []byte(fmt.Sprintf("<failed to read body: %v>", readErr))
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return "", fmt.Errorf("%w: the GitHub App private key (PEM) may be invalid or expired. Please check the CODE_COPIER_PEM secret in GCP Secret Manager. Response: %s", ErrAuthentication, string(body))
		}
		return "", fmt.Errorf("%w: GET %s: %d %s %s", ErrAuthentication, url, resp.StatusCode, resp.Status, body)
	}

	var installations []struct {
		ID      int64 `json:"id"`
		Account struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"account"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&installations); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	for _, inst := range installations {
		if inst.Account.Login == org {
			return fmt.Sprintf("%d", inst.ID), nil
		}
	}

	return "", fmt.Errorf("%w: %s", ErrInstallationNotFound, org)
}

// SetInstallationTokenForOrg sets a cached installation token for an organization.
// This is primarily used for testing to bypass the GitHub App authentication flow.
func SetInstallationTokenForOrg(org, token string) {
	defaultTokenManager.SetTokenForOrgNoExpiry(org, token)
}

// GetRestClientForOrg returns a GitHub REST API client authenticated for a specific organization.
func GetRestClientForOrg(ctx context.Context, config *configs.Config, org string) (*github.Client, error) {
	tm := defaultTokenManager
	hc := tm.GetHTTPClient()

	if token, ok := tm.GetTokenForOrg(org); ok {
		return newGitHubRESTClient(token, hc), nil
	}

	installationID, err := getInstallationIDForOrg(ctx, config, org)
	if err != nil {
		return nil, fmt.Errorf("failed to get installation ID for org %s: %w", org, err)
	}

	token, err := getOrRefreshJWT(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to get JWT: %w", err)
	}

	installationToken, expiresAt, err := getInstallationAccessToken(installationID, token, hc)
	if err != nil {
		return nil, fmt.Errorf("failed to get installation token for org %s: %w", org, err)
	}

	tm.SetTokenForOrg(org, installationToken, expiresAt)
	return newGitHubRESTClient(installationToken, hc), nil
}

// newGitHubRESTClient creates a GitHub REST client with the given token and base HTTP client.
func newGitHubRESTClient(token string, hc *http.Client) *github.Client {
	src := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	base := http.DefaultTransport
	if hc != nil && hc.Transport != nil {
		base = hc.Transport
	}
	httpClient := &http.Client{
		Transport: &oauth2.Transport{
			Source: src,
			Base:   base,
		},
	}
	return github.NewClient(httpClient)
}

// RoundTrip adds the Authorization header to each request.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	return http.DefaultTransport.RoundTrip(req)
}
