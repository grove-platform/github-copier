package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
)

// UpdateDeprecationFile updates the deprecation file with the provided data map.
func UpdateDeprecationFile(ctx context.Context, config *configs.Config, filesToDeprecate map[string]types.Configs) {
	// Early return if there are no files to deprecate - prevents blank commits
	if len(filesToDeprecate) == 0 {
		LogInfo("No deprecated files to record; skipping deprecation file update")
		return
	}

	// Fetch the deprecation file from the repository
	client := GetRestClient()

	fileContent, _, _, err := client.Repositories.GetContents(
		ctx,
		config.ConfigRepoOwner,
		config.ConfigRepoName,
		config.DeprecationFile,
		&github.RepositoryContentGetOptions{
			Ref: config.ConfigRepoBranch,
		},
	)
	if err != nil {
		LogError("Error getting deprecation file", "error", err)
		return
	}
	if fileContent == nil {
		LogError("Deprecation file content is nil")
		return
	}

	content, err := fileContent.GetContent()
	if err != nil {
		LogError("Error decoding deprecation file", "error", err)
		return
	}

	var deprecationFile types.DeprecationFile
	err = json.Unmarshal([]byte(content), &deprecationFile)
	if err != nil {
		LogError("Failed to unmarshal deprecation file", "file", config.DeprecationFile, "error", err)
		return
	}

	for key, value := range filesToDeprecate {
		newDeprecatedFileEntry := types.DeprecatedFileEntry{
			FileName:  key,
			Repo:      value.TargetRepo,
			Branch:    value.TargetBranch,
			DeletedOn: time.Now().Format(time.RFC3339),
		}
		deprecationFile = append(deprecationFile, newDeprecatedFileEntry)
	}

	updatedJSON, err := json.MarshalIndent(deprecationFile, "", "  ")
	if err != nil {
		LogError("Error marshaling JSON", "error", err)
		return
	}

	message := fmt.Sprintf("Updating %s.", config.DeprecationFile)
	uploadDeprecationFileChanges(ctx, config, message, string(updatedJSON))

	LogInfo("Successfully updated deprecation file", "file", config.DeprecationFile, "entries", len(filesToDeprecate))
}

func uploadDeprecationFileChanges(ctx context.Context, config *configs.Config, message string, newDeprecationFileContents string) {
	client := GetRestClient()

	targetFileContent, _, _, err := client.Repositories.GetContents(ctx, config.ConfigRepoOwner, config.ConfigRepoName,
		config.DeprecationFile, &github.RepositoryContentGetOptions{Ref: config.ConfigRepoBranch})

	if err != nil {
		LogError("Error getting deprecation file contents", "error", err)
		return
	}
	if targetFileContent == nil {
		LogError("Target deprecation file content is nil")
		return
	}

	options := &github.RepositoryContentFileOptions{
		Message: github.String(message),
		Content: []byte(newDeprecationFileContents),
		Branch:  github.String(config.ConfigRepoBranch),
		Committer: &github.CommitAuthor{Name: github.String(config.CommitterName),
			Email: github.String(config.CommitterEmail)},
	}

	options.SHA = targetFileContent.SHA
	_, _, err = client.Repositories.UpdateFile(ctx, config.ConfigRepoOwner, config.ConfigRepoName, config.DeprecationFile, options)
	if err != nil {
		LogError("Cannot update deprecation file", "error", err)
	}

	LogInfo("Deprecation file updated.")
}
