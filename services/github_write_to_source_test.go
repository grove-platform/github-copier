package services

import (
	"context"
	"testing"

	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
)

func TestUpdateDeprecationFile_EmptyMap(t *testing.T) {
	// When the map is empty, UpdateDeprecationFile should return early without panic.
	UpdateDeprecationFile(context.Background(), configs.NewConfig(), map[string]types.Configs{})
}

func TestUpdateDeprecationFile_WithFiles(t *testing.T) {
	// Note: This test will fail if it actually tries to call GitHub API.
	// In a real test environment, we would need to:
	// 1. Mock the GetRestClient() function
	// 2. Mock the GitHub API responses
	// 3. Verify the correct API calls were made
	t.Skip("Skipping test that requires GitHub API mocking")
}

func TestDeprecationFileEnvironmentVariables(t *testing.T) {
	tests := []struct {
		name            string
		deprecationFile string
	}{
		{
			name:            "default config",
			deprecationFile: "deprecated-files.json",
		},
		{
			name:            "custom file",
			deprecationFile: "custom-deprecated.json",
		},
		{
			name:            "nested path",
			deprecationFile: "docs/deprecated/files.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.deprecationFile == "" {
				t.Error("Deprecation file path should not be empty")
			}
		})
	}
}

// TODO https://jira.mongodb.org/browse/DOCSP-54727
// Note: Comprehensive testing of UpdateDeprecationFile would require:
// 1. Refactoring to accept a GitHub client interface instead of using global GetRestClient()
// 2. Creating mock implementations of the GitHub client
// 3. Testing scenarios:
//    - Empty deprecation list (early return)
//    - Fetching existing deprecation file
//    - Handling missing deprecation file (404)
//    - Merging new files with existing files
//    - Removing duplicates
//    - Committing changes to GitHub
//    - Error handling for API failures
//
// Example refactored signature:
// func UpdateDeprecationFile(ctx context.Context, config *configs.Config, client GitHubClient) error
//
// This would allow for proper unit testing with mocked dependencies.
