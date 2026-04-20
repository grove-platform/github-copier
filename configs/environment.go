package configs

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all environment configuration
type Config struct {
	EnvFile              string
	Port                 string
	ConfigRepoName       string // Repository where config file is stored
	ConfigRepoOwner      string // Owner of repository where config file is stored
	AppId                string
	AppClientId          string
	InstallationId       string
	CommitterName        string
	CommitterEmail       string
	ConfigFile           string
	MainConfigFile       string // Main config file with workflow references (optional)
	UseMainConfig        bool   // Whether to use main config format
	DeprecationFile      string
	WebserverPath        string
	ConfigRepoBranch     string // Branch to fetch config file from
	PEMKeyName           string
	WebhookSecretName    string
	WebhookSecret        string
	CopierLogName        string
	GoogleCloudProjectId string
	DefaultRecursiveCopy bool
	DefaultPRMerge       bool
	DefaultCommitMessage string

	// Optional features
	DryRun             bool
	AuditEnabled       bool
	MongoURI           string
	MongoURISecretName string
	AuditDatabase      string
	AuditCollection    string
	MetricsEnabled     bool

	// Slack notifications
	SlackWebhookURL      string
	SlackChannel         string
	SlackUsername        string
	SlackIconEmoji       string
	SlackEnabled         bool
	SlackPlainText       bool   // Use plain text only (required for Workflow Builder webhooks)
	SlackMessageVariable string // Variable name for Workflow Builder webhooks (default: "text")

	// GitHub API retry configuration
	GitHubAPIMaxRetries        int
	GitHubAPIInitialRetryDelay int // in milliseconds

	// PR merge polling configuration
	PRMergePollMaxAttempts int
	PRMergePollInterval    int // in milliseconds

	// Config cache TTL in seconds (0 = disabled)
	ConfigCacheTTLSeconds int

	// Webhook background processing timeout in seconds (0 = no timeout)
	WebhookProcessingTimeoutSeconds int

	// Webhook retry configuration
	WebhookMaxRetries        int // max retry attempts for failed webhook processing
	WebhookRetryInitialDelay int // initial delay between retries in seconds (doubles each attempt)

	// Operator web UI — off unless OPERATOR_UI_ENABLED=true (intended for local dev).
	OperatorUIEnabled           bool
	OperatorUIToken             string
	OperatorRepoSlug            string // "owner/repo" for GitHub links and optional tag API
	OperatorReleaseGitHubToken  string // PAT with contents:write to create a version tag (optional)
	OperatorReleaseTargetBranch string // branch SHA used when creating a tag (default main)
}

const (
	EnvFile                         = "ENV"
	Port                            = "PORT"
	ConfigRepoName                  = "CONFIG_REPO_NAME"
	ConfigRepoOwner                 = "CONFIG_REPO_OWNER"
	AppId                           = "GITHUB_APP_ID"
	AppClientId                     = "GITHUB_APP_CLIENT_ID"
	InstallationId                  = "INSTALLATION_ID"
	CommitterName                   = "COMMITTER_NAME"
	CommitterEmail                  = "COMMITTER_EMAIL"
	ConfigFile                      = "CONFIG_FILE"
	MainConfigFile                  = "MAIN_CONFIG_FILE"
	UseMainConfig                   = "USE_MAIN_CONFIG"
	DeprecationFile                 = "DEPRECATION_FILE"
	WebserverPath                   = "WEBSERVER_PATH"
	ConfigRepoBranch                = "CONFIG_REPO_BRANCH"
	PEMKeyName                      = "PEM_NAME"            // #nosec G101 -- env var name, not a credential
	WebhookSecretName               = "WEBHOOK_SECRET_NAME" // #nosec G101 -- env var name, not a credential
	WebhookSecret                   = "WEBHOOK_SECRET"      // #nosec G101 -- env var name, not a credential
	CopierLogName                   = "COPIER_LOG_NAME"
	GoogleCloudProjectId            = "GOOGLE_CLOUD_PROJECT_ID"
	DefaultRecursiveCopy            = "DEFAULT_RECURSIVE_COPY"
	DefaultPRMerge                  = "DEFAULT_PR_MERGE"
	DefaultCommitMessage            = "DEFAULT_COMMIT_MESSAGE"
	DryRun                          = "DRY_RUN"
	AuditEnabled                    = "AUDIT_ENABLED"
	MongoURI                        = "MONGO_URI"
	MongoURISecretName              = "MONGO_URI_SECRET_NAME" // #nosec G101 -- env var name, not a credential
	AuditDatabase                   = "AUDIT_DATABASE"
	AuditCollection                 = "AUDIT_COLLECTION"
	MetricsEnabled                  = "METRICS_ENABLED"
	SlackWebhookURL                 = "SLACK_WEBHOOK_URL" // #nosec G101 -- env var name, not a credential
	SlackChannel                    = "SLACK_CHANNEL"
	SlackUsername                   = "SLACK_USERNAME"
	SlackIconEmoji                  = "SLACK_ICON_EMOJI"
	SlackEnabled                    = "SLACK_ENABLED"
	SlackPlainText                  = "SLACK_PLAIN_TEXT"       // Use for Workflow Builder webhooks
	SlackMessageVariable            = "SLACK_MESSAGE_VARIABLE" // Variable name for Workflow Builder (default: "text")
	GitHubAPIMaxRetries             = "GITHUB_API_MAX_RETRIES"
	GitHubAPIInitialRetryDelay      = "GITHUB_API_INITIAL_RETRY_DELAY"
	PRMergePollMaxAttempts          = "PR_MERGE_POLL_MAX_ATTEMPTS"
	PRMergePollInterval             = "PR_MERGE_POLL_INTERVAL"
	ConfigCacheTTLSeconds           = "CONFIG_CACHE_TTL_SECONDS"
	WebhookProcessingTimeoutSeconds = "WEBHOOK_PROCESSING_TIMEOUT_SECONDS"
	WebhookMaxRetries               = "WEBHOOK_MAX_RETRIES"
	WebhookRetryInitialDelay        = "WEBHOOK_RETRY_INITIAL_DELAY" //nolint:gosec // env var name, not a credential
	OperatorUIEnabled               = "OPERATOR_UI_ENABLED"
	OperatorUIToken                 = "OPERATOR_UI_TOKEN" // #nosec G101 -- env var name
	OperatorRepoSlug                = "OPERATOR_REPO_SLUG"
	OperatorReleaseGitHubToken      = "OPERATOR_RELEASE_GITHUB_TOKEN" // #nosec G101 -- env var name
	OperatorReleaseTargetBranch     = "OPERATOR_RELEASE_TARGET_BRANCH"
)

// NewConfig returns a new Config instance with default values
func NewConfig() *Config {
	return &Config{
		Port:                            "8080",
		CommitterName:                   "Copier Bot",
		CommitterEmail:                  "bot@example.com",
		ConfigFile:                      "copier-config.yaml",
		DeprecationFile:                 "deprecated_examples.json",
		WebserverPath:                   "/events",
		ConfigRepoBranch:                "main",                               // Default branch to fetch config file from
		PEMKeyName:                      "CODE_COPIER_PEM",                    // short secret name; resolved to full path at runtime via SecretPath()
		WebhookSecretName:               "webhook-secret",                     // short secret name; resolved to full path at runtime via SecretPath()
		CopierLogName:                   "copy-copier-log",                    // default log name for logging to GCP
		GoogleCloudProjectId:            "github-copy-code-examples",          // default project ID for logging to GCP
		DefaultRecursiveCopy:            true,                                 // system-wide default for recursive copying that individual config entries can override.
		DefaultPRMerge:                  false,                                // system-wide default for PR merge without review that individual config entries can override.
		DefaultCommitMessage:            "Automated PR with updated examples", // default commit message used when per-config commit_message is absent.
		GitHubAPIMaxRetries:             3,                                    // default number of retry attempts for GitHub API calls
		GitHubAPIInitialRetryDelay:      500,                                  // default initial retry delay in milliseconds (exponential backoff)
		PRMergePollMaxAttempts:          20,                                   // default max attempts to poll PR for mergeability (~10 seconds with 500ms interval)
		PRMergePollInterval:             500,                                  // default polling interval in milliseconds
		ConfigCacheTTLSeconds:           300,                                  // default 5 minutes; set to 0 to disable caching
		WebhookProcessingTimeoutSeconds: 300,                                  // default 5 minutes; 0 = no timeout
		WebhookMaxRetries:               2,                                    // default 2 retries (3 total attempts)
		WebhookRetryInitialDelay:        5,                                    // default 5 seconds initial delay (doubles each retry)
	}
}

// LoadEnvironment loads environment variables and returns populated Config
func LoadEnvironment(envFile string) (*Config, error) {
	// Initialize with defaults
	config := NewConfig()

	// Set the provided env file
	config.EnvFile = envFile

	// Get current environment (default to test)
	env := getEnvWithDefault(EnvFile, "test")

	// Define env files to load in order of precedence
	envFiles := []string{
		".env",
		".env." + env,
	}

	if config.EnvFile != "" {
		envFiles = append(envFiles, config.EnvFile)
	}

	for _, file := range envFiles {
		if _, err := os.Stat(file); err == nil {
			if err = godotenv.Load(file); err != nil {
				return nil, fmt.Errorf("error loading env file %s: %w", file, err)
			}
		}
	}

	// Populate config from environment variables, with defaults where applicable
	config.Port = getEnvWithDefault(Port, config.Port)
	config.ConfigRepoName = os.Getenv(ConfigRepoName)
	config.ConfigRepoOwner = os.Getenv(ConfigRepoOwner)
	config.AppId = os.Getenv(AppId)
	config.AppClientId = os.Getenv(AppClientId)
	config.InstallationId = os.Getenv(InstallationId)
	config.CommitterName = getEnvWithDefault(CommitterName, config.CommitterName)
	config.CommitterEmail = getEnvWithDefault(CommitterEmail, config.CommitterEmail)
	config.ConfigFile = getEnvWithDefault(ConfigFile, config.ConfigFile)
	config.MainConfigFile = os.Getenv(MainConfigFile)
	config.UseMainConfig = getBoolEnvWithDefault(UseMainConfig, config.MainConfigFile != "")
	config.DeprecationFile = getEnvWithDefault(DeprecationFile, config.DeprecationFile)
	config.WebserverPath = getEnvWithDefault(WebserverPath, config.WebserverPath)
	config.ConfigRepoBranch = getEnvWithDefault(ConfigRepoBranch, config.ConfigRepoBranch)
	config.PEMKeyName = getEnvWithDefault(PEMKeyName, config.PEMKeyName)
	config.WebhookSecretName = getEnvWithDefault(WebhookSecretName, config.WebhookSecretName)
	config.WebhookSecret = os.Getenv(WebhookSecret)
	config.DefaultRecursiveCopy = getBoolEnvWithDefault(DefaultRecursiveCopy, config.DefaultRecursiveCopy)
	config.DefaultPRMerge = getBoolEnvWithDefault(DefaultPRMerge, config.DefaultPRMerge)
	config.CopierLogName = getEnvWithDefault(CopierLogName, config.CopierLogName)
	config.GoogleCloudProjectId = getEnvWithDefault(GoogleCloudProjectId, config.GoogleCloudProjectId)
	config.DefaultCommitMessage = getEnvWithDefault(DefaultCommitMessage, config.DefaultCommitMessage)

	// Optional features
	config.DryRun = getBoolEnvWithDefault(DryRun, false)
	config.AuditEnabled = getBoolEnvWithDefault(AuditEnabled, false)
	config.MongoURI = os.Getenv(MongoURI)
	config.MongoURISecretName = os.Getenv(MongoURISecretName)
	config.AuditDatabase = getEnvWithDefault(AuditDatabase, "copier_audit")
	config.AuditCollection = getEnvWithDefault(AuditCollection, "events")
	config.MetricsEnabled = getBoolEnvWithDefault(MetricsEnabled, true)
	config.WebhookSecret = os.Getenv(WebhookSecret)

	// Slack notifications
	config.SlackWebhookURL = os.Getenv(SlackWebhookURL)
	config.SlackChannel = getEnvWithDefault(SlackChannel, "#code-examples")
	config.SlackUsername = getEnvWithDefault(SlackUsername, "Examples Copier")
	config.SlackIconEmoji = getEnvWithDefault(SlackIconEmoji, ":robot_face:")
	config.SlackEnabled = getBoolEnvWithDefault(SlackEnabled, config.SlackWebhookURL != "")
	config.SlackPlainText = getBoolEnvWithDefault(SlackPlainText, false)
	config.SlackMessageVariable = getEnvWithDefault(SlackMessageVariable, "text")

	// GitHub API retry configuration
	config.GitHubAPIMaxRetries = getIntEnvWithDefault(GitHubAPIMaxRetries, config.GitHubAPIMaxRetries)
	config.GitHubAPIInitialRetryDelay = getIntEnvWithDefault(GitHubAPIInitialRetryDelay, config.GitHubAPIInitialRetryDelay)

	// PR merge polling configuration
	config.PRMergePollMaxAttempts = getIntEnvWithDefault(PRMergePollMaxAttempts, config.PRMergePollMaxAttempts)
	config.PRMergePollInterval = getIntEnvWithDefault(PRMergePollInterval, config.PRMergePollInterval)

	// Config cache
	config.ConfigCacheTTLSeconds = getIntEnvWithDefault(ConfigCacheTTLSeconds, config.ConfigCacheTTLSeconds)

	// Webhook processing
	config.WebhookProcessingTimeoutSeconds = getIntEnvWithDefault(WebhookProcessingTimeoutSeconds, config.WebhookProcessingTimeoutSeconds)
	config.WebhookMaxRetries = getIntEnvWithDefault(WebhookMaxRetries, config.WebhookMaxRetries)
	config.WebhookRetryInitialDelay = getIntEnvWithDefault(WebhookRetryInitialDelay, config.WebhookRetryInitialDelay)

	config.OperatorUIEnabled = getBoolEnvWithDefault(OperatorUIEnabled, false)
	config.OperatorUIToken = os.Getenv(OperatorUIToken)
	config.OperatorRepoSlug = os.Getenv(OperatorRepoSlug)
	config.OperatorReleaseGitHubToken = os.Getenv(OperatorReleaseGitHubToken)
	config.OperatorReleaseTargetBranch = getEnvWithDefault(OperatorReleaseTargetBranch, "main")

	if err := validateConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

// EffectiveConfigFile returns the config file path that the app will actually use.
// If MainConfigFile is set (USE_MAIN_CONFIG=true), it takes precedence over ConfigFile.
func (c *Config) EffectiveConfigFile() string {
	if c.UseMainConfig && c.MainConfigFile != "" {
		return c.MainConfigFile
	}
	return c.ConfigFile
}

// SecretPath resolves a secret name to a fully-qualified GCP Secret Manager resource path.
// If the name already contains "projects/", it is returned as-is (for backward compatibility).
// Otherwise, it builds the full path using the configured GoogleCloudProjectId.
func (c *Config) SecretPath(secretName string) string {
	if strings.HasPrefix(secretName, "projects/") {
		return secretName
	}
	return fmt.Sprintf("projects/%s/secrets/%s/versions/latest", c.GoogleCloudProjectId, secretName)
}

// getEnvWithDefault returns the environment variable value or default if not set
func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

// getBoolEnvWithDefault returns the boolean environment variable value or default if not set
func getBoolEnvWithDefault(key string, defaultValue bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return strings.ToLower(value) == "true"
}

// getIntEnvWithDefault returns the integer environment variable value or default if not set
func getIntEnvWithDefault(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	var intValue int
	if _, err := fmt.Sscanf(value, "%d", &intValue); err != nil {
		return defaultValue
	}
	return intValue
}

// validateConfig checks if all required configuration values are set
func validateConfig(config *Config) error {
	var missingVars []string

	requiredVars := map[string]string{
		ConfigRepoName:  config.ConfigRepoName,
		ConfigRepoOwner: config.ConfigRepoOwner,
		AppId:           config.AppId,
		InstallationId:  config.InstallationId,
	}

	for name, value := range requiredVars {
		if value == "" {
			missingVars = append(missingVars, name)
		}
	}

	if len(missingVars) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missingVars, ", "))
	}

	// Warn if webhook secret is not configured.
	// In production, webhook signature verification should always be enabled
	// to prevent unauthorized requests from being processed.
	env := getEnvWithDefault(EnvFile, "")
	if config.WebhookSecret == "" && config.WebhookSecretName == "" {
		if env == "production" || env == "prod" {
			return fmt.Errorf("WEBHOOK_SECRET or WEBHOOK_SECRET_NAME is required in production to enable webhook signature verification")
		}
	}

	if err := validateWebserverPath(config.WebserverPath); err != nil {
		return err
	}

	return nil
}

// validateWebserverPath rejects values that would collide with built-in HTTP routes.
func validateWebserverPath(p string) error {
	p = strings.TrimSpace(p)
	if p == "" {
		return fmt.Errorf("WEBSERVER_PATH cannot be empty")
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("WEBSERVER_PATH must start with / (got %q)", p)
	}
	if p == "/" {
		return fmt.Errorf("WEBSERVER_PATH cannot be / (reserved; use a dedicated path such as /events)")
	}
	for _, reserved := range []string{"/health", "/ready", "/metrics", "/config", "/operator"} {
		if strings.EqualFold(p, reserved) {
			return fmt.Errorf("WEBSERVER_PATH cannot be %s (reserved for a built-in route)", reserved)
		}
	}
	norm := strings.TrimSuffix(strings.ToLower(p), "/") + "/"
	if strings.HasPrefix(norm, "/operator/") {
		return fmt.Errorf("WEBSERVER_PATH cannot be under /operator/ (reserved for the operator UI)")
	}
	return nil
}
