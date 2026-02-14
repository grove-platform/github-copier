package services

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
)

// parseRepoPath parses a repository path in the format "owner/repo" and returns owner and repo separately.
// If the path doesn't contain a slash, it returns defaultOwner and the path as repo name.
func parseRepoPath(repoPath string, defaultOwner string) (owner, repo string) {
	parts := strings.Split(repoPath, "/")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	// Fallback to default owner if no slash found (backward compatibility)
	return defaultOwner, repoPath
}

// normalizeRepoName ensures a repository name includes the owner prefix.
// If the repo name already has an owner (contains "/"), returns it as-is.
// Otherwise, prepends the defaultOwner.
func normalizeRepoName(repoName string, defaultOwner string) string {
	if strings.Contains(repoName, "/") {
		return repoName
	}
	return defaultOwner + "/" + repoName
}

// normalizeRefPath ensures a ref path is in the correct format for different GitHub API calls.
// For GetRef: expects "heads/main" (no "refs/" prefix)
// For UpdateRef: expects "refs/heads/main" (full ref path)
func normalizeRefPath(branchPath string, fullPath bool) string {
	// Strip "refs/" prefix if present
	refPath := strings.TrimPrefix(branchPath, "refs/")

	// Ensure "heads/" prefix exists (unless it's a tag)
	if !strings.HasPrefix(refPath, "heads/") && !strings.HasPrefix(refPath, "tags/") {
		refPath = "heads/" + refPath
	}

	// Add "refs/" prefix back if full path is needed
	if fullPath {
		return "refs/" + refPath
	}
	return refPath
}

// AddFilesToTargetRepos uploads files to target repository branches.
// It accepts the upload map as a parameter for concurrency safety.
func AddFilesToTargetRepos(ctx context.Context, config *configs.Config, filesToUpload map[types.UploadKey]types.UploadFileContent, prTemplateFetcher PRTemplateFetcher, metricsCollector *MetricsCollector) {
	for key, value := range filesToUpload {
		// Parse the repository to get the organization
		owner, _ := parseRepoPath(key.RepoName, config.ConfigRepoOwner)

		// Get a client authenticated for this organization
		client, err := GetRestClientForOrg(ctx, config, owner)
		if err != nil {
			LogCritical(fmt.Sprintf("Failed to get GitHub client for org %s: %v", owner, err))
			// Record failure for each file in this batch
			if metricsCollector != nil {
				for range value.Content {
					metricsCollector.RecordFileUploadFailed()
				}
			}
			continue
		}

		// Determine commit strategy from value (set by pattern-matching system)
		strategy := string(value.CommitStrategy)
		if strategy == "" {
			strategy = "direct" // default
		}

		// Get commit message from value or use default
		commitMsg := value.CommitMessage
		if strings.TrimSpace(commitMsg) == "" {
			commitMsg = config.DefaultCommitMessage
		}

		// Get PR title from value or use commit message
		prTitle := value.PRTitle
		if strings.TrimSpace(prTitle) == "" {
			prTitle = commitMsg
		}

		// Get PR body from value
		prBody := value.PRBody

		// Fetch and merge PR template if requested
		if value.UsePRTemplate && prTemplateFetcher != nil && strategy != "direct" {
			targetBranch := strings.TrimPrefix(key.BranchPath, "refs/heads/")
			template, err := prTemplateFetcher.FetchPRTemplate(ctx, client, key.RepoName, targetBranch)
			if err != nil {
				LogWarning(fmt.Sprintf("Failed to fetch PR template for %s: %v", key.RepoName, err))
			} else if template != "" {
				// Merge configured body with template
				prBody = MergePRBodyWithTemplate(prBody, template)
				LogInfo(fmt.Sprintf("Merged PR template for %s", key.RepoName))
			}
		}

		// Get auto-merge setting from value
		mergeWithoutReview := value.AutoMergePR

		switch strategy {
		case "direct": // commits directly to the target branch
			LogInfo(fmt.Sprintf("Using direct commit strategy for %s on branch %s", key.RepoName, key.BranchPath))
			if err := addFilesToBranch(ctx, config, client, key, value.Content, commitMsg); err != nil {
				LogCritical(fmt.Sprintf("Failed to add files to target branch: %v\n", err))
				// Record failure for each file in this batch
				if metricsCollector != nil {
					for range value.Content {
						metricsCollector.RecordFileUploadFailed()
					}
				}
			}
		default: // "pr" or "pull_request" strategy
			LogInfo(fmt.Sprintf("Using PR commit strategy for %s on branch %s (auto_merge=%v)", key.RepoName, key.BranchPath, mergeWithoutReview))
			if err := addFilesViaPR(ctx, config, client, key, value.Content, commitMsg, prTitle, prBody, mergeWithoutReview); err != nil {
				LogCritical(fmt.Sprintf("Failed via PR path: %v\n", err))
				// Record failure for each file in this batch
				if metricsCollector != nil {
					for range value.Content {
						metricsCollector.RecordFileUploadFailed()
					}
				}
			}
		}
	}
}

// createPullRequest opens a pull request from head to base in the specified repository.
func createPullRequest(ctx context.Context, client *github.Client, defaultOwner, repo, head, base, title, body string) (*github.PullRequest, error) {
	owner, repoName := parseRepoPath(repo, defaultOwner)
	pr := &github.NewPullRequest{
		Title: github.String(title),
		Head:  github.String(head), // for same-repo branches, just "branch"; for forks, use "owner:branch"
		Base:  github.String(base), // e.g. "main"
		Body:  github.String(body),
	}
	created, _, err := client.PullRequests.Create(ctx, owner, repoName, pr)
	if err != nil {
		return nil, fmt.Errorf("could not create PR: %w", err)
	}
	return created, nil
}

// addFilesViaPR creates a temporary branch, commits files to it using the provided commitMessage,
// opens a pull request with prTitle and prBody, and optionally merges it automatically.
func addFilesViaPR(ctx context.Context, config *configs.Config, client *github.Client, key types.UploadKey,
	files []github.RepositoryContent, commitMessage string, prTitle string, prBody string, mergeWithoutReview bool,
) error {
	defaultOwner := config.ConfigRepoOwner
	tempBranch := "copier/" + time.Now().UTC().Format("20060102-150405")

	// 1) Create branch off the target branch specified in key.BranchPath or default to "main"
	baseBranch := strings.TrimPrefix(key.BranchPath, "refs/heads/")
	newRef, err := createBranch(ctx, client, defaultOwner, key.RepoName, tempBranch, baseBranch)
	if err != nil {
		return fmt.Errorf("create branch: %w", err)
	}
	_ = newRef // we just need it created; ref is not reused directly

	// 2) Commit files to temp branch
	entries := make(map[string]string, len(files))
	for _, f := range files {
		content, err := f.GetContent()
		if err != nil {
			return fmt.Errorf("decode content for %s: %w", f.GetName(), err)
		}
		entries[f.GetName()] = content
	}

	tempKey := types.UploadKey{RepoName: key.RepoName, BranchPath: "refs/heads/" + tempBranch}
	treeSHA, baseSHA, err := createCommitTree(ctx, config, client, tempKey, entries)
	if err != nil {
		return fmt.Errorf("create tree on temp branch: %w", err)
	}
	if err = createCommit(ctx, client, defaultOwner, tempKey, baseSHA, treeSHA, commitMessage); err != nil {
		return fmt.Errorf("create commit on temp branch: %w", err)
	}

	// 3) Create PR from temp branch to base branch
	base := strings.TrimPrefix(key.BranchPath, "refs/heads/")
	pr, err := createPullRequest(ctx, client, defaultOwner, key.RepoName, tempBranch, base, prTitle, prBody)
	if err != nil {
		return fmt.Errorf("create PR: %w", err)
	}

	// 4) Optionally merge the PR without review if MergeWithoutReview is true
	LogInfo(fmt.Sprintf("PR created: #%d from %s to %s", pr.GetNumber(), tempBranch, base))
	LogInfo(fmt.Sprintf("PR URL: %s", pr.GetHTMLURL()))
	if mergeWithoutReview {
		// Poll PR for mergeability; GitHub may take a moment to compute it
		maxAttempts := config.PRMergePollMaxAttempts
		pollInterval := config.PRMergePollInterval

		var mergeable *bool
		var mergeableState string
		owner, repoName := parseRepoPath(key.RepoName, defaultOwner)
		for i := 0; i < maxAttempts; i++ {
			current, _, gerr := client.PullRequests.Get(ctx, owner, repoName, pr.GetNumber())
			if gerr == nil && current != nil {
				mergeable = current.Mergeable
				mergeableState = current.GetMergeableState()
				if mergeable != nil { // computed
					break
				}
			}
			time.Sleep(time.Duration(pollInterval) * time.Millisecond)
		}
		if mergeable != nil && !*mergeable || strings.EqualFold(mergeableState, "dirty") {
			LogWarning(fmt.Sprintf("PR #%d is not mergeable (state=%s). Likely merge conflicts. Leaving PR open for manual resolution.", pr.GetNumber(), mergeableState))
			return fmt.Errorf("%w: pull request #%d has conflicts (state=%s)", ErrMergeConflict, pr.GetNumber(), mergeableState)
		}
		if err = mergePR(ctx, client, defaultOwner, key.RepoName, pr.GetNumber()); err != nil {
			return fmt.Errorf("merge PR: %w", err)
		}
		if err = deleteBranchIfExists(ctx, client, defaultOwner, key.RepoName, &github.Reference{Ref: github.String("refs/heads/" + tempBranch)}); err != nil {
			// Log but don't fail - branch cleanup is not critical
			LogWarning(fmt.Sprintf("Failed to delete temp branch after merge: %v", err))
		}
	} else {
		LogInfo(fmt.Sprintf("PR created and awaiting review: #%d", pr.GetNumber()))
	}
	return nil
}

// addFilesToBranch builds a tree, creates a commit, and updates the ref (direct to target branch)
func addFilesToBranch(ctx context.Context, config *configs.Config, client *github.Client, key types.UploadKey,
	files []github.RepositoryContent, message string) error {

	entries := make(map[string]string, len(files))
	for _, f := range files {
		content, err := f.GetContent()
		if err != nil {
			return fmt.Errorf("decode content for %s: %w", f.GetName(), err)
		}
		entries[f.GetName()] = content
	}

	treeSHA, baseSHA, err := createCommitTree(ctx, config, client, key, entries)
	if err != nil {
		LogCritical(fmt.Sprintf("Error creating commit tree: %v\n", err))
		return err
	}
	if err := createCommit(ctx, client, config.ConfigRepoOwner, key, baseSHA, treeSHA, message); err != nil {
		LogCritical(fmt.Sprintf("Error creating commit: %v\n", err))
		return err
	}
	return nil
}

// createBranch creates a new branch from the specified base branch (defaults to 'main') and deletes it first if it already exists.
func createBranch(ctx context.Context, client *github.Client, defaultOwner, repo, newBranch string, baseBranch ...string) (*github.Reference, error) {
	// Normalize repo name for consistent logging and operations
	normalizedRepo := normalizeRepoName(repo, defaultOwner)
	owner, repoName := parseRepoPath(normalizedRepo, defaultOwner)

	// Use provided base branch or default to "main"
	base := "main"
	if len(baseBranch) > 0 && baseBranch[0] != "" {
		base = baseBranch[0]
	}

	baseRef, _, err := client.Git.GetRef(ctx, owner, repoName, "refs/heads/"+base)
	if err != nil {
		LogCritical(fmt.Sprintf("Failed to get '%s' baseRef: %s", base, err))
		return nil, err
	}

	// Check if branch already exists and delete it (404 is expected when it doesn't exist)
	newBranchRef, _, _ := client.Git.GetRef(ctx, owner, repoName, fmt.Sprintf("%s%s", "refs/heads/", newBranch)) //nolint:errcheck // 404 expected
	if err := deleteBranchIfExists(ctx, client, defaultOwner, normalizedRepo, newBranchRef); err != nil {
		return nil, fmt.Errorf("failed to delete existing branch %s: %w", newBranch, err)
	}

	createRef := github.CreateRef{
		Ref: fmt.Sprintf("refs/heads/%s", newBranch),
		SHA: baseRef.Object.GetSHA(),
	}

	newBranchRef, _, err = client.Git.CreateRef(ctx, owner, repoName, createRef)
	if err != nil {
		LogCritical(fmt.Sprintf("Failed to create newBranchRef %s: %s", createRef.Ref, err))
		return nil, err
	}

	LogInfo(fmt.Sprintf("Branch created successfully: %s on %s (from %s)", createRef.Ref, normalizedRepo, base))

	return newBranchRef, nil
}

// createCommitTree looks up the branch ref once, then builds a tree on top of that base commit.
func createCommitTree(ctx context.Context, config *configs.Config, client *github.Client, targetBranch types.UploadKey,
	files map[string]string) (treeSHA string, baseSHA string, err error) {

	defaultOwner := config.ConfigRepoOwner
	// Normalize repo name for consistent logging
	normalizedRepo := normalizeRepoName(targetBranch.RepoName, defaultOwner)
	owner, repoName := parseRepoPath(normalizedRepo, defaultOwner)
	LogInfo(fmt.Sprintf("DEBUG createCommitTree: targetBranch.RepoName=%q, normalized=%q, parsed owner=%q, repoName=%q",
		targetBranch.RepoName, normalizedRepo, owner, repoName))

	// 1) Get current ref with retry logic to handle GitHub API eventual consistency
	// When a branch is just created, it may take a moment to be visible
	var ref *github.Reference

	maxRetries := config.GitHubAPIMaxRetries
	retryDelay := time.Duration(config.GitHubAPIInitialRetryDelay) * time.Millisecond

	// GetRef expects "heads/main" format (no "refs/" prefix)
	refPath := normalizeRefPath(targetBranch.BranchPath, false)

	for attempt := 1; attempt <= maxRetries; attempt++ {
		ref, _, err = client.Git.GetRef(ctx, owner, repoName, refPath)
		if err == nil && ref != nil {
			break // Success
		}

		if attempt < maxRetries {
			LogWarning(fmt.Sprintf("Failed to get ref for %s (attempt %d/%d): %v. Retrying in %v...",
				normalizedRepo, attempt, maxRetries, err, retryDelay))
			time.Sleep(retryDelay)
			retryDelay *= 2 // Exponential backoff
		}
	}

	if err != nil || ref == nil {
		if err == nil {
			err = fmt.Errorf("targetRef is nil after %d attempts", maxRetries)
		}
		LogCritical(fmt.Sprintf("Failed to get ref for %s after %d attempts: %v\n", normalizedRepo, maxRetries, err))
		return "", "", err
	}
	baseSHA = ref.GetObject().GetSHA()

	// 2) Build tree entries
	var treeEntries []*github.TreeEntry
	for path, content := range files {
		treeEntries = append(treeEntries, &github.TreeEntry{
			Path:    github.String(path),
			Type:    github.String("blob"),
			Mode:    github.String("100644"),
			Content: github.String(content),
		})
	}

	// 3) Create tree on top of baseSHA
	tree, _, err := client.Git.CreateTree(ctx, owner, repoName, baseSHA, treeEntries)
	if err != nil {
		return "", "", fmt.Errorf("failed to create tree: %w", err)
	}
	return tree.GetSHA(), baseSHA, nil
}

// createCommit makes the commit using the provided baseSHA, and updates the branch ref to the new commit.
func createCommit(ctx context.Context, client *github.Client, defaultOwner string, targetBranch types.UploadKey,
	baseSHA string, treeSHA string, message string) error {

	owner, repoName := parseRepoPath(targetBranch.RepoName, defaultOwner)

	parent := &github.Commit{SHA: github.Ptr(baseSHA)}
	commit := github.Commit{
		Message: github.Ptr(message),
		Tree:    &github.Tree{SHA: github.Ptr(treeSHA)},
		Parents: []*github.Commit{parent},
	}

	newCommit, _, err := client.Git.CreateCommit(ctx, owner, repoName, commit, nil)
	if err != nil {
		return fmt.Errorf("could not create commit: %w", err)
	}

	// Update branch ref directly (no second GET)
	// UpdateRef expects ref path like "heads/main" (without "refs/" prefix)
	fullRefPath := normalizeRefPath(targetBranch.BranchPath, true)
	refPath := strings.TrimPrefix(fullRefPath, "refs/")
	updateRef := github.UpdateRef{
		SHA:   newCommit.GetSHA(),
		Force: github.Ptr(false),
	}
	if _, _, err := client.Git.UpdateRef(ctx, owner, repoName, refPath, updateRef); err != nil {
		// Detect non-fast-forward / conflict scenarios and provide a clearer error
		if eresp, ok := err.(*github.ErrorResponse); ok {
			if eresp.Response != nil && eresp.Response.StatusCode == http.StatusUnprocessableEntity {
				return fmt.Errorf("%w: failed to update ref: non-fast-forward. Consider using PR strategy: %v", ErrMergeConflict, err)
			}
		}
		return fmt.Errorf("failed to update ref to new commit: %w", err)
	}
	return nil
}

// mergePR merges the specified pull request in the given repository.
func mergePR(ctx context.Context, client *github.Client, defaultOwner, repo string, pr_number int) error {
	owner, repoName := parseRepoPath(repo, defaultOwner)

	options := &github.PullRequestOptions{
		MergeMethod: "merge", // Other options: "squash" or "rebase"
	}
	result, _, err := client.PullRequests.Merge(ctx, owner, repoName, pr_number, "Merging the pull request", options)
	if err != nil {
		LogCritical(fmt.Sprintf("Failed to merge PR: %v\n", err))
		return err
	}
	if result.GetMerged() {
		LogInfo(fmt.Sprintf("Successfully merged PR #%d\n", pr_number))
		return nil
	} else {
		LogError(fmt.Sprintf("Failed to merge PR #%d: %s", pr_number, result.GetMessage()))
		return fmt.Errorf("failed to merge PR #%d: %s", pr_number, result.GetMessage())
	}
}

// deleteBranchIfExists deletes the specified branch if it exists, except for 'main'.
// Returns an error if attempting to delete the main branch or if deletion fails.
func deleteBranchIfExists(backgroundContext context.Context, client *github.Client, defaultOwner, repo string, ref *github.Reference) error {
	// Early return if ref is nil (branch doesn't exist)
	if ref == nil {
		return nil
	}

	// Normalize repo name for consistent logging
	normalizedRepo := normalizeRepoName(repo, defaultOwner)
	owner, repoName := parseRepoPath(normalizedRepo, defaultOwner)

	if ref.GetRef() == "refs/heads/main" {
		LogError("I refuse to delete branch 'main'.")
		return fmt.Errorf("refusing to delete protected branch 'main'")
	}

	LogInfo(fmt.Sprintf("Deleting branch %s on %s", ref.GetRef(), normalizedRepo))
	_, _, err := client.Git.GetRef(backgroundContext, owner, repoName, ref.GetRef())

	if err == nil { // Branch exists (there was no error fetching it)
		_, err = client.Git.DeleteRef(backgroundContext, owner, repoName, ref.GetRef())
		if err != nil {
			LogCritical(fmt.Sprintf("Error deleting branch: %v\n", err))
			return fmt.Errorf("failed to delete branch %s: %w", ref.GetRef(), err)
		}
	}
	return nil
}

// DeleteBranchIfExistsExported is an exported wrapper for testing deleteBranchIfExists
func DeleteBranchIfExistsExported(ctx context.Context, client *github.Client, defaultOwner, repo string, ref *github.Reference) error {
	return deleteBranchIfExists(ctx, client, defaultOwner, repo, ref)
}

// parseIntWithDefault parses a string to int, returning defaultValue on error
func parseIntWithDefault(s string, defaultValue int) (int, error) {
	var result int
	if _, err := fmt.Sscanf(s, "%d", &result); err != nil {
		return defaultValue, err
	}
	return result, nil
}
