package services

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-github/v82/github"
	"gopkg.in/yaml.v3"

	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
)

// ConfigLoader handles loading and parsing configuration files
type ConfigLoader interface {
	LoadConfig(ctx context.Context, config *configs.Config) (*types.YAMLConfig, error)
	LoadConfigFromContent(content string, filename string) (*types.YAMLConfig, error)
}

// DefaultConfigLoader implements the ConfigLoader interface for the legacy
// single-file config format. Deprecated: migrate to the main config format
// (USE_MAIN_CONFIG=true) and use DefaultMainConfigLoader instead.
type DefaultConfigLoader struct{}

// NewConfigLoader creates a new legacy config loader.
// Deprecated: use NewMainConfigLoader instead.
func NewConfigLoader() ConfigLoader {
	return &DefaultConfigLoader{}
}

// LoadConfig loads configuration from the repository or local file
func (cl *DefaultConfigLoader) LoadConfig(ctx context.Context, config *configs.Config) (*types.YAMLConfig, error) {
	var content string
	var err error

	// Try to load from local file first (for testing)
	content, err = loadLocalConfigFile(config.ConfigFile)
	if err == nil {
		LogInfoCtx(ctx, "loaded config from local file", map[string]interface{}{
			"file": config.ConfigFile,
		})
	} else {
		// Fall back to fetching from repository
		content, err = retrieveConfigFileContent(ctx, config.ConfigFile, config)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve config file: %w", err)
		}
	}

	return cl.LoadConfigFromContent(content, config.ConfigFile)
}

// LoadConfigFromContent loads configuration from a string
func (cl *DefaultConfigLoader) LoadConfigFromContent(content string, filename string) (*types.YAMLConfig, error) {
	if content == "" {
		return nil, fmt.Errorf("%w: config file is empty", ErrConfigLoad)
	}

	// Parse as YAML (supports both YAML and JSON since YAML is a superset of JSON)
	var yamlConfig types.YAMLConfig
	err := yaml.Unmarshal([]byte(content), &yamlConfig)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to parse config file: %v", ErrConfigLoad, err)
	}

	// Set defaults
	yamlConfig.SetDefaults()

	// Validate
	if err := yamlConfig.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrConfigValidation, err)
	}

	return &yamlConfig, nil
}

// retrieveConfigFileContent fetches the config file content from the repository
func retrieveConfigFileContent(ctx context.Context, filePath string, config *configs.Config) (string, error) {
	// Get GitHub client for the config repo's org (auto-discovers installation ID)
	client, err := GetRestClientForOrg(ctx, config, config.ConfigRepoOwner)
	if err != nil {
		return "", fmt.Errorf("failed to get GitHub client for org %s: %w", config.ConfigRepoOwner, err)
	}

	// Fetch file content
	fileContent, _, _, err := client.Repositories.GetContents(
		ctx,
		config.ConfigRepoOwner,
		config.ConfigRepoName,
		filePath,
		&github.RepositoryContentGetOptions{
			Ref: config.ConfigRepoBranch,
		},
	)
	if err != nil {
		// Check if this is an authentication error
		errStr := err.Error()
		if strings.Contains(errStr, "401") || strings.Contains(errStr, "Bad credentials") {
			return "", fmt.Errorf("%w: unable to fetch config file. The GitHub App private key (PEM) may be invalid or expired. Please check the CODE_COPIER_PEM secret in GCP Secret Manager. Original error: %v", ErrAuthentication, err)
		}
		return "", fmt.Errorf("failed to get config file: %w", err)
	}
	if fileContent == nil {
		return "", fmt.Errorf("%w: config file at path: %s", ErrContentNil, filePath)
	}

	// Decode content
	content, err := fileContent.GetContent()
	if err != nil {
		return "", fmt.Errorf("failed to decode config file: %w", err)
	}

	return content, nil
}

// loadLocalConfigFile attempts to load config from a local file
// This is useful for local testing and development
func loadLocalConfigFile(filename string) (string, error) {
	// Try to read from current directory
	data, err := os.ReadFile(filename) // #nosec G304 -- local dev config path from caller
	if err != nil {
		return "", err
	}
	return string(data), nil
}
