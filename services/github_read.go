package services

import (
	"context"
	"fmt"

	"github.com/google/go-github/v48/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
	"github.com/shurcooL/githubv4"
)

// GetFilesChangedInPr retrieves the list of files changed in a specified pull request.
// It returns a slice of ChangedFile structures containing details about each changed file.
// Parameters:
//   - owner: The repository owner (e.g., "mongodb")
//   - repo: The repository name (e.g., "docs-sample-apps")
//   - pr_number: The pull request number
func GetFilesChangedInPr(ctx context.Context, config *configs.Config, owner string, repo string, pr_number int) ([]types.ChangedFile, error) {
	if defaultTokenManager.GetInstallationAccessToken() == "" {
		LogWarning("No installation token provided, configuring permissions")
		if err := ConfigurePermissions(ctx, config); err != nil {
			return nil, fmt.Errorf("failed to configure permissions: %w", err)
		}
	}

	// Use org-specific client to ensure we have the right installation token
	client, err := GetGraphQLClientForOrg(ctx, config, owner)
	if err != nil {
		return nil, fmt.Errorf("failed to get GraphQL client for org %s: %w", owner, err)
	}

	var changedFiles []types.ChangedFile
	var cursor *githubv4.String = nil
	hasNextPage := true

	// Paginate through all files
	for hasNextPage {
		var prQuery types.PullRequestQuery
		variables := map[string]interface{}{
			"owner":  githubv4.String(owner),
			"name":   githubv4.String(repo),
			"number": githubv4.Int(pr_number),
			"cursor": cursor,
		}

		err := client.Query(ctx, &prQuery, variables)
		if err != nil {
			LogCritical(fmt.Sprintf("Failed to execute query GetFilesChanged: %v", err))
			return nil, err
		}

		// Append files from this page
		for _, edge := range prQuery.Repository.PullRequest.Files.Edges {
			changedFiles = append(changedFiles, types.ChangedFile{
				Path:      string(edge.Node.Path),
				Additions: int(edge.Node.Additions),
				Deletions: int(edge.Node.Deletions),
				Status:    string(edge.Node.ChangeType),
			})
		}

		// Check if there are more pages
		hasNextPage = prQuery.Repository.PullRequest.Files.PageInfo.HasNextPage
		if hasNextPage {
			cursor = &prQuery.Repository.PullRequest.Files.PageInfo.EndCursor
		}
	}

	LogInfoCtx(ctx, "Retrieved changed files from PR", map[string]interface{}{
		"file_count": len(changedFiles),
	})

	return changedFiles, nil
}

// RetrieveFileContents fetches the contents of a file from the config repository at the specified path.
// It returns a github.RepositoryContent object containing the file details.
func RetrieveFileContents(ctx context.Context, config *configs.Config, filePath string) (github.RepositoryContent, error) {
	owner := config.ConfigRepoOwner
	repo := config.ConfigRepoName
	client := GetRestClient()

	fileContent, _, _, err :=
		client.Repositories.GetContents(ctx, owner, repo,
			filePath, &github.RepositoryContentGetOptions{
				Ref: config.ConfigRepoBranch,
			})

	if err != nil {
		return github.RepositoryContent{}, fmt.Errorf("failed to get file content for %s: %w", filePath, err)
	}
	if fileContent == nil {
		return github.RepositoryContent{}, fmt.Errorf("%w: %s", ErrContentNil, filePath)
	}
	return *fileContent, nil
}
