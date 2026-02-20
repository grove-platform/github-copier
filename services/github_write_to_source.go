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

// UpdateDeprecationFile updates the deprecation file in the source repo with the provided data map.
// The deprecation file tracks which files have been deleted from the source repo.
func UpdateDeprecationFile(ctx context.Context, config *configs.Config, filesToDeprecate map[string]types.Configs, sourceRepoOwner, sourceRepoName, sourceBranch string) {
	// Early return if there are no files to deprecate - prevents blank commits
	if len(filesToDeprecate) == 0 {
		LogInfo("No deprecated files to record; skipping deprecation file update")
		return
	}

	sourceRepo := fmt.Sprintf("%s/%s", sourceRepoOwner, sourceRepoName)

	if config.DryRun {
		LogInfo("[DRY-RUN] Would update deprecation file",
			"file", config.DeprecationFile,
			"source_repo", sourceRepo,
			"source_branch", sourceBranch,
			"deprecated_count", len(filesToDeprecate),
		)
		for path := range filesToDeprecate {
			LogInfo("[DRY-RUN] Would mark as deprecated", "path", path)
		}
		return
	}

	// Fetch the deprecation file from the source repository
	client := GetRestClient()

	fileContent, _, _, err := client.Repositories.GetContents(
		ctx,
		sourceRepoOwner,
		sourceRepoName,
		config.DeprecationFile,
		&github.RepositoryContentGetOptions{
			Ref: sourceBranch,
		},
	)

	var deprecationFile types.DeprecationFile

	if err != nil {
		// If file doesn't exist, start with empty array
		LogInfo("Deprecation file not found, will create new one",
			"file", config.DeprecationFile,
			"source_repo", sourceRepo,
		)
		deprecationFile = types.DeprecationFile{}
	} else if fileContent == nil {
		LogError("Deprecation file content is nil")
		return
	} else {
		content, err := fileContent.GetContent()
		if err != nil {
			LogError("Error decoding deprecation file", "error", err)
			return
		}

		err = json.Unmarshal([]byte(content), &deprecationFile)
		if err != nil {
			LogError("Failed to unmarshal deprecation file", "file", config.DeprecationFile, "error", err)
			return
		}
	}

	// Build a set of existing entries for duplicate detection
	// Key format: "filename|repo|branch" to identify unique entries
	existingEntries := make(map[string]bool)
	for _, entry := range deprecationFile {
		key := entry.FileName + "|" + entry.Repo + "|" + entry.Branch
		existingEntries[key] = true
	}

	entriesAdded := 0
	for key, value := range filesToDeprecate {
		// Check for duplicates before appending (prevents issues with webhook redelivery)
		entryKey := key + "|" + value.TargetRepo + "|" + value.TargetBranch
		if existingEntries[entryKey] {
			LogInfo("Skipping duplicate deprecation entry",
				"filename", key,
				"repo", value.TargetRepo,
				"branch", value.TargetBranch,
			)
			continue
		}

		newDeprecatedFileEntry := types.DeprecatedFileEntry{
			FileName:  key,
			Repo:      value.TargetRepo,
			Branch:    value.TargetBranch,
			DeletedOn: time.Now().Format(time.RFC3339),
		}
		deprecationFile = append(deprecationFile, newDeprecatedFileEntry)
		existingEntries[entryKey] = true // Mark as added to prevent duplicates within current batch
		entriesAdded++
	}

	// Early return if all entries were duplicates
	if entriesAdded == 0 {
		LogInfo("All deprecation entries already exist; skipping update")
		return
	}

	updatedJSON, err := json.MarshalIndent(deprecationFile, "", "  ")
	if err != nil {
		LogError("Error marshaling JSON", "error", err)
		return
	}

	message := fmt.Sprintf("Updating %s.", config.DeprecationFile)
	uploadDeprecationFileChanges(ctx, config, message, string(updatedJSON), sourceRepoOwner, sourceRepoName, sourceBranch, fileContent)

	LogInfo("Successfully updated deprecation file",
		"file", config.DeprecationFile,
		"source_repo", sourceRepo,
		"entries", len(filesToDeprecate),
	)
}

func uploadDeprecationFileChanges(ctx context.Context, config *configs.Config, message string, newDeprecationFileContents string, sourceRepoOwner, sourceRepoName, sourceBranch string, existingContent *github.RepositoryContent) {
	client := GetRestClient()

	options := &github.RepositoryContentFileOptions{
		Message: github.Ptr(message),
		Content: []byte(newDeprecationFileContents),
		Branch:  github.Ptr(sourceBranch),
		Committer: &github.CommitAuthor{
			Name:  github.Ptr(config.CommitterName),
			Email: github.Ptr(config.CommitterEmail),
		},
	}

	var err error
	if existingContent != nil && existingContent.SHA != nil {
		// Update existing file
		options.SHA = existingContent.SHA
		_, _, err = client.Repositories.UpdateFile(ctx, sourceRepoOwner, sourceRepoName, config.DeprecationFile, options)
	} else {
		// Create new file
		_, _, err = client.Repositories.CreateFile(ctx, sourceRepoOwner, sourceRepoName, config.DeprecationFile, options)
	}

	if err != nil {
		LogError("Cannot update deprecation file",
			"error", err,
			"source_repo", fmt.Sprintf("%s/%s", sourceRepoOwner, sourceRepoName),
		)
		return
	}

	LogInfo("Deprecation file updated.",
		"source_repo", fmt.Sprintf("%s/%s", sourceRepoOwner, sourceRepoName),
		"branch", sourceBranch,
	)
}
