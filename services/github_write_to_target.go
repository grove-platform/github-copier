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
// When auditLogger is non-nil, each file copy is recorded (success or failure) for MongoDB audit.
func AddFilesToTargetRepos(ctx context.Context, config *configs.Config, filesToUpload map[types.UploadKey]types.UploadFileContent, prTemplateFetcher PRTemplateFetcher, metricsCollector *MetricsCollector, auditLogger AuditLogger) {
	if config.DryRun {
		for key, value := range filesToUpload {
			LogInfo("[DRY-RUN] Would upload files to target repo",
				"repo", key.RepoName,
				"branch", key.BranchPath,
				"file_count", len(value.Content),
				"strategy", value.CommitStrategy,
			)
			for path := range value.Content {
				LogInfo("[DRY-RUN] Would write file", "repo", key.RepoName, "path", path)
			}
		}
		return
	}

	for key, value := range filesToUpload {
		batchStart := time.Now()
		if err := uploadToTarget(ctx, config, key, value, prTemplateFetcher); err != nil {
			LogCritical("Failed to upload files", "repo", key.RepoName, "error", err)
			recordBatchFailure(metricsCollector, len(value.Content))
			auditLogCopyBatchFailure(ctx, auditLogger, key, value, err)
		} else {
			auditLogCopyBatchSuccess(ctx, auditLogger, key, value, time.Since(batchStart))
		}
	}
}

func auditLogCopyBatchSuccess(ctx context.Context, auditLogger AuditLogger, key types.UploadKey, value types.UploadFileContent, elapsed time.Duration) {
	if auditLogger == nil || len(value.Content) == 0 {
		return
	}
	n := len(value.Content)
	perFileMs := elapsed.Milliseconds() / int64(n)
	if perFileMs == 0 && elapsed > 0 {
		perFileMs = 1
	}
	for i := range value.Content {
		f := value.Content[i]
		meta := types.CopierFileMeta{}
		if i < len(value.FileMeta) {
			meta = value.FileMeta[i]
		}
		srcPath := meta.SourcePath
		if srcPath == "" {
			srcPath = f.GetPath()
		}
		ev := &AuditEvent{
			RuleName:   meta.RuleName,
			SourceRepo: meta.SourceRepo,
			SourcePath: srcPath,
			TargetRepo: key.RepoName,
			TargetPath: f.GetName(),
			PRNumber:   meta.PRNumber,
			Success:    true,
			DurationMs: perFileMs,
			FileSize:   int64(decodedFileBytes(&f)),
		}
		if err := auditLogger.LogCopyEvent(ctx, ev); err != nil {
			LogWarning("audit LogCopyEvent failed", "error", err)
		}
	}
}

func auditLogCopyBatchFailure(ctx context.Context, auditLogger AuditLogger, key types.UploadKey, value types.UploadFileContent, batchErr error) {
	if auditLogger == nil || len(value.Content) == 0 {
		return
	}
	msg := batchErr.Error()
	for i := range value.Content {
		f := value.Content[i]
		meta := types.CopierFileMeta{}
		if i < len(value.FileMeta) {
			meta = value.FileMeta[i]
		}
		srcPath := meta.SourcePath
		if srcPath == "" {
			srcPath = f.GetPath()
		}
		ev := &AuditEvent{
			RuleName:     meta.RuleName,
			SourceRepo:   meta.SourceRepo,
			SourcePath:   srcPath,
			TargetRepo:   key.RepoName,
			TargetPath:   f.GetName(),
			PRNumber:     meta.PRNumber,
			Success:      false,
			ErrorMessage: msg,
		}
		if err := auditLogger.LogCopyEvent(ctx, ev); err != nil {
			LogWarning("audit LogCopyEvent (failure) failed", "error", err)
		}
	}
}

func decodedFileBytes(f *github.RepositoryContent) int {
	if f == nil {
		return 0
	}
	c, err := f.GetContent()
	if err != nil {
		return 0
	}
	return len(c)
}

// uploadToTarget handles a single upload-key: authenticates for the target org,
// resolves commit parameters, and dispatches to the appropriate strategy.
func uploadToTarget(ctx context.Context, config *configs.Config, key types.UploadKey, value types.UploadFileContent, prTemplateFetcher PRTemplateFetcher) error {
	owner, _ := parseRepoPath(key.RepoName, config.ConfigRepoOwner)

	client, err := GetRestClientForOrg(ctx, config, owner)
	if err != nil {
		return fmt.Errorf("get GitHub client for org %s: %w", owner, err)
	}

	params := resolveCommitParams(config, key, value, prTemplateFetcher, client, ctx)

	switch params.strategy {
	case "direct":
		LogInfo("Using direct commit strategy",
			"repo", key.RepoName,
			"branch", key.BranchPath,
			"strategy_source", key.CommitStrategy,
			"file_count", len(value.Content),
		)
		return addFilesToBranch(ctx, config, client, key, value.Content, params.commitMsg)
	default: // "pr" or "pull_request"
		LogInfo("Using PR commit strategy",
			"repo", key.RepoName,
			"branch", key.BranchPath,
			"strategy_source", key.CommitStrategy,
			"file_count", len(value.Content),
			"auto_merge", params.mergeWithoutReview,
		)
		return addFilesViaPR(ctx, config, client, key, value.Content, params.commitMsg, params.prTitle, params.prBody, params.mergeWithoutReview)
	}
}

// commitParams groups the resolved parameters for a single upload operation.
type commitParams struct {
	strategy           string
	commitMsg          string
	prTitle            string
	prBody             string
	mergeWithoutReview bool
}

// resolveCommitParams derives commit strategy, message, PR title/body, and template
// from the upload value and config defaults.
func resolveCommitParams(config *configs.Config, key types.UploadKey, value types.UploadFileContent, prTemplateFetcher PRTemplateFetcher, client *github.Client, ctx context.Context) commitParams {
	strategy := string(value.CommitStrategy)
	if strategy == "" {
		strategy = "direct"
	}

	commitMsg := value.CommitMessage
	if strings.TrimSpace(commitMsg) == "" {
		commitMsg = config.DefaultCommitMessage
	}

	prTitle := value.PRTitle
	if strings.TrimSpace(prTitle) == "" {
		prTitle = commitMsg
	}

	prBody := value.PRBody
	if value.UsePRTemplate && prTemplateFetcher != nil && strategy != "direct" {
		targetBranch := strings.TrimPrefix(key.BranchPath, "refs/heads/")
		template, err := prTemplateFetcher.FetchPRTemplate(ctx, client, key.RepoName, targetBranch)
		if err != nil {
			LogWarning("Failed to fetch PR template", "repo", key.RepoName, "error", err)
		} else if template != "" {
			prBody = MergePRBodyWithTemplate(prBody, template)
			LogInfo("Merged PR template", "repo", key.RepoName)
		}
	}

	return commitParams{
		strategy:           strategy,
		commitMsg:          commitMsg,
		prTitle:            prTitle,
		prBody:             prBody,
		mergeWithoutReview: value.AutoMergePR,
	}
}

// recordBatchFailure records n file upload failures on the metrics collector.
func recordBatchFailure(mc *MetricsCollector, n int) {
	if mc == nil {
		return
	}
	for i := 0; i < n; i++ {
		mc.RecordFileUploadFailed()
	}
}

// createPullRequest opens a pull request from head to base in the specified repository.
func createPullRequest(ctx context.Context, client *github.Client, defaultOwner, repo, head, base, title, body string) (*github.PullRequest, error) {
	owner, repoName := parseRepoPath(repo, defaultOwner)
	pr := &github.NewPullRequest{
		Title: github.Ptr(title),
		Head:  github.Ptr(head), // for same-repo branches, just "branch"; for forks, use "owner:branch"
		Base:  github.Ptr(base), // e.g. "main"
		Body:  github.Ptr(body),
	}
	created, _, err := client.PullRequests.Create(ctx, owner, repoName, pr)
	if err != nil {
		return nil, fmt.Errorf("could not create PR: %w", err)
	}
	return created, nil
}

// findExistingCopierPR searches for an open PR whose head branch starts with "copier/"
// targeting the given base branch. Returns nil if none found.
func findExistingCopierPR(ctx context.Context, client *github.Client, owner, repoName, baseBranch string) *github.PullRequest {
	prs, _, err := client.PullRequests.List(ctx, owner, repoName, &github.PullRequestListOptions{
		State: "open",
		Base:  baseBranch,
		ListOptions: github.ListOptions{
			PerPage: 50,
		},
	})
	if err != nil {
		LogWarning("Failed to list PRs for dedup check; will create new PR", "repo", owner+"/"+repoName, "error", err)
		return nil
	}
	for _, pr := range prs {
		if strings.HasPrefix(pr.GetHead().GetRef(), "copier/") {
			return pr
		}
	}
	return nil
}

// addFilesViaPR creates a temporary branch, commits files to it using the provided commitMessage,
// opens a pull request with prTitle and prBody, and optionally merges it automatically.
// If an existing open PR from a copier/* branch is found, the files are pushed to that
// branch and the PR is updated instead of creating a duplicate.
func addFilesViaPR(ctx context.Context, config *configs.Config, client *github.Client, key types.UploadKey,
	files []github.RepositoryContent, commitMessage string, prTitle string, prBody string, mergeWithoutReview bool,
) error {
	defaultOwner := config.ConfigRepoOwner
	baseBranch := strings.TrimPrefix(key.BranchPath, "refs/heads/")
	owner, repoName := parseRepoPath(key.RepoName, defaultOwner)

	// 0. Check for an existing open copier PR targeting this base branch.
	existingPR := findExistingCopierPR(ctx, client, owner, repoName, baseBranch)
	if existingPR != nil {
		existingBranch := existingPR.GetHead().GetRef()
		LogInfo("Found existing open copier PR; updating instead of creating new",
			"pr_number", existingPR.GetNumber(),
			"branch", existingBranch,
			"repo", key.RepoName,
		)

		// Push new files to the existing branch
		if err := commitFilesToBranch(ctx, config, client, key, files, existingBranch, commitMessage); err != nil {
			return fmt.Errorf("commit to existing copier branch %s: %w", existingBranch, err)
		}

		// Update the PR title/body to reflect the latest content
		_, _, err := client.PullRequests.Edit(ctx, owner, repoName, existingPR.GetNumber(), &github.PullRequest{
			Title: github.Ptr(prTitle),
			Body:  github.Ptr(prBody),
		})
		if err != nil {
			LogWarning("Failed to update existing PR title/body", "pr_number", existingPR.GetNumber(), "error", err)
		}

		if mergeWithoutReview {
			return autoMergePR(ctx, config, client, key.RepoName, defaultOwner, existingPR.GetNumber(), existingBranch)
		}
		LogInfo("Existing PR updated and awaiting review", "pr_number", existingPR.GetNumber())
		return nil
	}

	// No existing PR — create a new temp branch and PR.
	tempBranch := "copier/" + time.Now().UTC().Format("20060102-150405")

	// 1. Create branch off the target
	if _, err := createBranch(ctx, client, defaultOwner, key.RepoName, tempBranch, baseBranch); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}

	// 2. Commit files to temp branch
	if err := commitFilesToBranch(ctx, config, client, key, files, tempBranch, commitMessage); err != nil {
		return err
	}

	// 3. Open PR from temp branch → base branch
	pr, err := createPullRequest(ctx, client, defaultOwner, key.RepoName, tempBranch, baseBranch, prTitle, prBody)
	if err != nil {
		return fmt.Errorf("create PR: %w", err)
	}

	LogInfo("PR created", "pr_number", pr.GetNumber(), "from_branch", tempBranch, "base_branch", baseBranch)
	LogInfo("PR URL", "url", pr.GetHTMLURL())

	// 4. Optionally auto-merge and clean up
	if mergeWithoutReview {
		return autoMergePR(ctx, config, client, key.RepoName, defaultOwner, pr.GetNumber(), tempBranch)
	}
	LogInfo("PR created and awaiting review", "pr_number", pr.GetNumber())
	return nil
}

// commitFilesToBranch decodes file contents and creates a tree + commit on the temp branch.
// If the resulting tree is identical to the branch's current tree, the commit is skipped.
func commitFilesToBranch(ctx context.Context, config *configs.Config, client *github.Client, key types.UploadKey,
	files []github.RepositoryContent, tempBranch string, commitMessage string,
) error {
	entries := make(map[string]string, len(files))
	for _, f := range files {
		content, err := f.GetContent()
		if err != nil {
			return fmt.Errorf("decode content for %s: %w", f.GetName(), err)
		}
		entries[f.GetName()] = content
	}

	tempKey := types.UploadKey{RepoName: key.RepoName, BranchPath: "refs/heads/" + tempBranch}
	tr, err := createCommitTree(ctx, config, client, tempKey, entries)
	if err != nil {
		return fmt.Errorf("create tree on temp branch: %w", err)
	}

	if tr.TreeSHA == tr.BaseTreeSHA {
		LogInfo("Skipping empty commit on temp branch — tree unchanged",
			"repo", key.RepoName,
			"branch", tempBranch,
			"tree_sha", tr.TreeSHA,
		)
		return nil
	}

	if err = createCommit(ctx, client, config.ConfigRepoOwner, tempKey, tr.BaseSHA, tr.TreeSHA, commitMessage); err != nil {
		return fmt.Errorf("create commit on temp branch: %w", err)
	}
	return nil
}

// autoMergePR polls the PR for mergeability, merges it, and deletes the temp branch.
func autoMergePR(ctx context.Context, config *configs.Config, client *github.Client, repo string, defaultOwner string, prNumber int, tempBranch string) error {
	owner, repoName := parseRepoPath(repo, defaultOwner)

	mergeable, state := pollMergeability(ctx, client, owner, repoName, prNumber, config.PRMergePollMaxAttempts, config.PRMergePollInterval)
	if mergeable != nil && !*mergeable || strings.EqualFold(state, "dirty") {
		LogWarning("PR is not mergeable; leaving open for manual resolution", "pr_number", prNumber, "state", state)
		return fmt.Errorf("%w: pull request #%d has conflicts (state=%s)", ErrMergeConflict, prNumber, state)
	}

	if err := mergePR(ctx, client, defaultOwner, repo, prNumber); err != nil {
		return fmt.Errorf("merge PR: %w", err)
	}

	if err := deleteBranchIfExists(ctx, client, defaultOwner, repo, &github.Reference{Ref: github.Ptr("refs/heads/" + tempBranch)}); err != nil {
		LogWarning("Failed to delete temp branch after merge", "error", err)
	}
	return nil
}

// pollMergeability polls the GitHub API until the PR's mergeability is computed or attempts are exhausted.
func pollMergeability(ctx context.Context, client *github.Client, owner string, repo string, prNumber int, maxAttempts int, pollIntervalMs int) (mergeable *bool, state string) {
	for i := 0; i < maxAttempts; i++ {
		current, _, err := client.PullRequests.Get(ctx, owner, repo, prNumber)
		if err == nil && current != nil {
			mergeable = current.Mergeable
			state = current.GetMergeableState()
			if mergeable != nil {
				return
			}
		}
		time.Sleep(time.Duration(pollIntervalMs) * time.Millisecond)
	}
	return
}

// addFilesToBranch builds a tree, creates a commit, and updates the ref (direct to target branch).
// If the resulting tree is identical to the current HEAD tree, the commit is skipped to avoid
// empty commits (e.g., when a duplicate webhook creates changes already at HEAD).
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

	tr, err := createCommitTree(ctx, config, client, key, entries)
	if err != nil {
		LogCritical("Error creating commit tree", "error", err)
		return err
	}

	if tr.TreeSHA == tr.BaseTreeSHA {
		LogInfo("Skipping empty commit — new tree is identical to HEAD tree",
			"repo", key.RepoName,
			"branch", key.BranchPath,
			"tree_sha", tr.TreeSHA,
		)
		return nil
	}

	if err := createCommit(ctx, client, config.ConfigRepoOwner, key, tr.BaseSHA, tr.TreeSHA, message); err != nil {
		LogCritical("Error creating commit", "error", err)
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
		LogCritical("Failed to get baseRef", "base", base, "error", err)
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
		LogCritical("Failed to create newBranchRef", "ref", createRef.Ref, "error", err)
		return nil, err
	}

	LogInfo("Branch created successfully", "ref", createRef.Ref, "repo", normalizedRepo, "base", base)

	return newBranchRef, nil
}

// treeResult holds the output of createCommitTree so callers can detect no-op trees.
type treeResult struct {
	TreeSHA     string // SHA of the newly created tree
	BaseSHA     string // SHA of the base commit (parent for the new commit)
	BaseTreeSHA string // SHA of the base commit's tree — if equal to TreeSHA, nothing changed
}

// createCommitTree looks up the branch ref once, then builds a tree on top of that base commit.
func createCommitTree(ctx context.Context, config *configs.Config, client *github.Client, targetBranch types.UploadKey,
	files map[string]string) (treeResult, error) {

	defaultOwner := config.ConfigRepoOwner
	// Normalize repo name for consistent logging
	normalizedRepo := normalizeRepoName(targetBranch.RepoName, defaultOwner)
	owner, repoName := parseRepoPath(normalizedRepo, defaultOwner)
	LogInfo("DEBUG createCommitTree", "target_repo_name", targetBranch.RepoName, "normalized", normalizedRepo, "owner", owner, "repo_name", repoName)

	// 1) Get current ref with retry logic to handle GitHub API eventual consistency
	// When a branch is just created, it may take a moment to be visible
	var ref *github.Reference
	var err error

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
			LogWarning("Failed to get ref; retrying", "repo", normalizedRepo, "attempt", attempt, "max_retries", maxRetries, "error", err, "retry_delay", retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2 // Exponential backoff
		}
	}

	if err != nil || ref == nil {
		if err == nil {
			err = fmt.Errorf("targetRef is nil after %d attempts", maxRetries)
		}
		LogCritical("Failed to get ref after max attempts", "repo", normalizedRepo, "attempts", maxRetries, "error", err)
		return treeResult{}, err
	}
	baseSHA := ref.GetObject().GetSHA()

	// 1b) Fetch the base commit to get its tree SHA (for no-op detection)
	baseCommit, _, err := client.Git.GetCommit(ctx, owner, repoName, baseSHA)
	if err != nil {
		return treeResult{}, fmt.Errorf("failed to get base commit %s: %w", baseSHA, err)
	}
	baseTreeSHA := baseCommit.GetTree().GetSHA()

	// 2) Build tree entries
	var treeEntries []*github.TreeEntry
	for path, content := range files {
		treeEntries = append(treeEntries, &github.TreeEntry{
			Path:    github.Ptr(path),
			Type:    github.Ptr("blob"),
			Mode:    github.Ptr("100644"),
			Content: github.Ptr(content),
		})
	}

	// 3) Create tree on top of baseSHA
	tree, _, err := client.Git.CreateTree(ctx, owner, repoName, baseSHA, treeEntries)
	if err != nil {
		return treeResult{}, fmt.Errorf("failed to create tree: %w", err)
	}
	return treeResult{
		TreeSHA:     tree.GetSHA(),
		BaseSHA:     baseSHA,
		BaseTreeSHA: baseTreeSHA,
	}, nil
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
		LogCritical("Failed to merge PR", "error", err)
		return err
	}
	if result.GetMerged() {
		LogInfo("Successfully merged PR", "pr_number", pr_number)
		return nil
	} else {
		LogError("Failed to merge PR", "pr_number", pr_number, "message", result.GetMessage())
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

	LogInfo("Deleting branch", "ref", ref.GetRef(), "repo", normalizedRepo)
	_, _, err := client.Git.GetRef(backgroundContext, owner, repoName, ref.GetRef())

	if err == nil { // Branch exists (there was no error fetching it)
		_, err = client.Git.DeleteRef(backgroundContext, owner, repoName, ref.GetRef())
		if err != nil {
			LogCritical("Error deleting branch", "error", err)
			return fmt.Errorf("failed to delete branch %s: %w", ref.GetRef(), err)
		}
	}
	return nil
}

// DeleteBranchIfExistsExported is an exported wrapper for testing deleteBranchIfExists
func DeleteBranchIfExistsExported(ctx context.Context, client *github.Client, defaultOwner, repo string, ref *github.Reference) error {
	return deleteBranchIfExists(ctx, client, defaultOwner, repo, ref)
}
