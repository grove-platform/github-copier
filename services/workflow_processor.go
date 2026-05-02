package services

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
	"golang.org/x/sync/errgroup"
)

// WorkflowProcessor processes workflows and applies transformations
type WorkflowProcessor interface {
	ProcessWorkflow(ctx context.Context, workflow types.Workflow, changedFiles []types.ChangedFile, prNumber int, sourceCommitSHA string) error
}

// workflowProcessor implements WorkflowProcessor
type workflowProcessor struct {
	patternMatcher   PatternMatcher
	pathTransformer  PathTransformer
	fileStateService FileStateService
	metricsCollector *MetricsCollector
	messageTemplater MessageTemplater
	config           *configs.Config
}

// NewWorkflowProcessor creates a new workflow processor
func NewWorkflowProcessor(
	patternMatcher PatternMatcher,
	pathTransformer PathTransformer,
	fileStateService FileStateService,
	metricsCollector *MetricsCollector,
	messageTemplater MessageTemplater,
	config *configs.Config,
) WorkflowProcessor {
	return &workflowProcessor{
		patternMatcher:   patternMatcher,
		pathTransformer:  pathTransformer,
		fileStateService: fileStateService,
		metricsCollector: metricsCollector,
		messageTemplater: messageTemplater,
		config:           config,
	}
}

// matchResult holds the outcome of the match phase for a single file.
type matchResult struct {
	workflow        types.Workflow
	file            types.ChangedFile
	targetPath      string
	isDelete        bool
	prNumber        int
	sourceCommitSHA string
	fileContent     *github.RepositoryContent // populated by fetch phase
}

// maxConcurrentFetches limits parallel GitHub API calls per workflow to avoid
// hitting secondary rate limits.
const maxConcurrentFetches = 5

// ProcessWorkflow processes a single workflow in three phases:
//  1. Match — identify files that match transformations (fast, no I/O)
//  2. Fetch — retrieve file contents from GitHub in parallel
//  3. Queue — add fetched files to the upload queue (sequential, mutates shared state)
func (wp *workflowProcessor) ProcessWorkflow(
	ctx context.Context,
	workflow types.Workflow,
	changedFiles []types.ChangedFile,
	prNumber int,
	sourceCommitSHA string,
) error {
	LogInfoCtx(ctx, "Processing workflow", map[string]interface{}{
		"workflow_name":    workflow.Name,
		"source_repo":      workflow.Source.Repo,
		"destination_repo": workflow.Destination.Repo,
		"file_count":       len(changedFiles),
	})

	// ── Phase 1: Match ───────────────────────────────────────────────────
	var matches []matchResult
	filesSkipped := 0

	for _, file := range changedFiles {
		mr, matched := wp.matchFile(ctx, workflow, file, prNumber, sourceCommitSHA)
		if !matched {
			filesSkipped++
			continue
		}
		matches = append(matches, mr)
	}

	if len(matches) == 0 {
		LogInfoCtx(ctx, "Workflow processing complete", map[string]interface{}{
			"workflow_name": workflow.Name,
			"files_matched": 0,
			"files_skipped": filesSkipped,
		})
		return nil
	}

	// ── Phase 2: Fetch (parallel) ────────────────────────────────────────
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentFetches)

	var fetchErrMu sync.Mutex
	var fetchErrors []error

	for i := range matches {
		if matches[i].isDelete {
			continue
		}
		mr := &matches[i]
		g.Go(func() error {
			fc, err := wp.fetchFileContent(gctx, workflow, mr.file, mr.sourceCommitSHA)
			if err != nil {
				fetchErrMu.Lock()
				fetchErrors = append(fetchErrors, fmt.Errorf("fetch %s: %w", mr.file.Path, err))
				fetchErrMu.Unlock()
				return nil // don't abort sibling fetches
			}
			mr.fileContent = fc
			return nil
		})
	}
	_ = g.Wait()

	for _, fe := range fetchErrors {
		LogErrorCtx(ctx, "Failed to fetch file content", fe, map[string]interface{}{
			"workflow_name": workflow.Name,
		})
	}

	// ── Phase 3: Queue (sequential) ──────────────────────────────────────
	filesMatched := 0
	for i := range matches {
		mr := &matches[i]
		if mr.isDelete {
			wp.addToDeprecationMap(mr.workflow, mr.targetPath, mr.file.Path, mr.prNumber)
			filesMatched++
			continue
		}
		if mr.fileContent == nil {
			continue // fetch failed — already logged
		}
		mr.fileContent.Name = github.Ptr(mr.targetPath)
		wp.queueUpload(ctx, mr.workflow, mr.fileContent, mr.targetPath, mr.prNumber, mr.sourceCommitSHA, mr.file.Path)
		filesMatched++
	}

	LogInfoCtx(ctx, "Workflow processing complete", map[string]interface{}{
		"workflow_name": workflow.Name,
		"files_matched": filesMatched,
		"files_skipped": filesSkipped,
	})

	return nil
}

// matchFile checks exclusions and transformations for a single file.
// Returns a matchResult and true if the file matched a transformation.
func (wp *workflowProcessor) matchFile(
	ctx context.Context,
	workflow types.Workflow,
	file types.ChangedFile,
	prNumber int,
	sourceCommitSHA string,
) (matchResult, bool) {
	if wp.isExcluded(file.Path, workflow.Exclude) {
		LogInfoCtx(ctx, "File excluded by workflow exclude patterns", map[string]interface{}{
			"workflow_name": workflow.Name,
			"file_path":     file.Path,
		})
		return matchResult{}, false
	}

	for i, transformation := range workflow.Transformations {
		matched, targetPath, err := wp.applyTransformation(ctx, workflow, transformation, file.Path)
		if err != nil {
			LogErrorCtx(ctx, "Failed to apply transformation", err, map[string]interface{}{
				"workflow_name":      workflow.Name,
				"transformation_idx": i,
				"file_path":          file.Path,
			})
			return matchResult{}, false
		}

		if !matched {
			continue
		}

		LogInfoCtx(ctx, "File matched transformation", map[string]interface{}{
			"workflow_name":       workflow.Name,
			"transformation_idx":  i,
			"transformation_type": transformation.GetType(),
			"source_path":         file.Path,
			"target_path":         targetPath,
		})

		isDelete := file.Status == "DELETED" || file.Status == "removed"
		return matchResult{
			workflow:        workflow,
			file:            file,
			targetPath:      targetPath,
			isDelete:        isDelete,
			prNumber:        prNumber,
			sourceCommitSHA: sourceCommitSHA,
		}, true
	}

	LogInfoCtx(ctx, "File did not match any transformation", map[string]interface{}{
		"workflow_name": workflow.Name,
		"file_path":     file.Path,
	})
	return matchResult{}, false
}

// fetchFileContent retrieves a single file's content from the source repository.
func (wp *workflowProcessor) fetchFileContent(
	ctx context.Context,
	workflow types.Workflow,
	file types.ChangedFile,
	sourceCommitSHA string,
) (*github.RepositoryContent, error) {
	parts := strings.Split(workflow.Source.Repo, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid source repo format: expected owner/repo, got: %s", workflow.Source.Repo)
	}
	return RetrieveFileContentsWithConfigAndBranch(ctx, wp.config, file.Path, sourceCommitSHA, parts[0], parts[1])
}

// applyTransformation applies a transformation to a file path
func (wp *workflowProcessor) applyTransformation(
	ctx context.Context,
	workflow types.Workflow,
	transformation types.Transformation,
	sourcePath string,
) (matched bool, targetPath string, err error) {
	switch transformation.GetType() {
	case types.TransformationTypeMove:
		return wp.applyMoveTransformation(transformation.Move, sourcePath)
	case types.TransformationTypeCopy:
		return wp.applyCopyTransformation(transformation.Copy, sourcePath)
	case types.TransformationTypeGlob:
		return wp.applyGlobTransformation(transformation.Glob, sourcePath)
	case types.TransformationTypeRegex:
		return wp.applyRegexTransformation(transformation.Regex, sourcePath)
	default:
		return false, "", fmt.Errorf("unknown transformation type: %s", transformation.GetType())
	}
}

// applyMoveTransformation applies a move transformation
func (wp *workflowProcessor) applyMoveTransformation(
	move *types.MoveTransform,
	sourcePath string,
) (matched bool, targetPath string, err error) {
	// Check if source path starts with the "from" prefix
	from := strings.TrimSuffix(move.From, "/")

	if sourcePath == from {
		// Exact match - move the file to the "to" path
		return true, move.To, nil
	}

	if strings.HasPrefix(sourcePath, from+"/") {
		// Path is under the "from" directory - preserve relative path
		relativePath := strings.TrimPrefix(sourcePath, from+"/")
		targetPath = filepath.Join(move.To, relativePath)
		return true, targetPath, nil
	}

	return false, "", nil
}

// applyCopyTransformation applies a copy transformation
func (wp *workflowProcessor) applyCopyTransformation(
	copy *types.CopyTransform,
	sourcePath string,
) (matched bool, targetPath string, err error) {
	// Copy only matches exact file path
	if sourcePath == copy.From {
		return true, copy.To, nil
	}
	return false, "", nil
}

// applyGlobTransformation applies a glob transformation
func (wp *workflowProcessor) applyGlobTransformation(
	glob *types.GlobTransform,
	sourcePath string,
) (matched bool, targetPath string, err error) {
	// Use doublestar for glob matching
	matched, err = doublestar.Match(glob.Pattern, sourcePath)
	if err != nil {
		return false, "", fmt.Errorf("invalid glob pattern: %w", err)
	}
	if !matched {
		return false, "", nil
	}

	// Extract variables for path transformation
	variables := wp.extractGlobVariables(glob.Pattern, sourcePath)

	// Apply path transformation using the correct signature
	targetPath, err = wp.pathTransformer.Transform(sourcePath, glob.Transform, variables)
	if err != nil {
		return false, "", fmt.Errorf("path transformation failed: %w", err)
	}

	return true, targetPath, nil
}

// applyRegexTransformation applies a regex transformation
func (wp *workflowProcessor) applyRegexTransformation(
	regex *types.RegexTransform,
	sourcePath string,
) (matched bool, targetPath string, err error) {
	// Use existing pattern matcher for regex
	sourcePattern := types.SourcePattern{
		Type:    types.PatternTypeRegex,
		Pattern: regex.Pattern,
	}

	matchResult := wp.patternMatcher.Match(sourcePath, sourcePattern)
	if !matchResult.Matched {
		return false, "", nil
	}

	// Apply path transformation with captured variables
	targetPath, err = wp.pathTransformer.Transform(sourcePath, regex.Transform, matchResult.Variables)
	if err != nil {
		return false, "", fmt.Errorf("path transformation failed: %w", err)
	}

	return true, targetPath, nil
}

// extractGlobVariables extracts variables from a glob pattern match
func (wp *workflowProcessor) extractGlobVariables(pattern, path string) map[string]string {
	variables := make(map[string]string)

	// Extract common variables
	// For pattern "mflix/server/**" matching "mflix/server/java-spring/src/main.java"
	// Extract relative_path = "java-spring/src/main.java"

	// Find the ** in the pattern
	starStarIdx := strings.Index(pattern, "**")
	if starStarIdx >= 0 {
		prefix := pattern[:starStarIdx]
		if strings.HasPrefix(path, prefix) {
			relativePath := strings.TrimPrefix(path, prefix)
			relativePath = strings.TrimPrefix(relativePath, "/")
			variables["relative_path"] = relativePath
		}
	}

	return variables
}

// isExcluded checks if a file path matches any exclude pattern (using regex)
func (wp *workflowProcessor) isExcluded(path string, excludePatterns []string) bool {
	for _, pattern := range excludePatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			LogWarning("Invalid exclude regex pattern", "pattern", pattern, "error", err)
			continue
		}
		if re.MatchString(path) {
			return true
		}
	}
	return false
}

// addToDeprecationMap adds a file to the deprecation map if deprecation tracking is enabled
func (wp *workflowProcessor) addToDeprecationMap(workflow types.Workflow, targetPath string, sourcePath string, prNumber int) {
	// Only track deprecations if explicitly enabled
	if workflow.DeprecationCheck == nil || !workflow.DeprecationCheck.Enabled {
		return
	}

	deprecationFile := "deprecated_examples.json"
	if workflow.DeprecationCheck.File != "" {
		deprecationFile = workflow.DeprecationCheck.File
	}

	entry := types.DeprecatedFileEntry{
		FileName:   targetPath,
		Repo:       workflow.Destination.Repo,
		Branch:     workflow.Destination.Branch,
		SourcePath: sourcePath,
		PRNumber:   prNumber,
	}

	wp.fileStateService.AddFileToDeprecate(deprecationFile, entry)
}

// queueUpload adds a pre-fetched file to the upload queue. The fileContent must
// already have its Name set to the target path.
func (wp *workflowProcessor) queueUpload(
	ctx context.Context,
	workflow types.Workflow,
	fileContent *github.RepositoryContent,
	targetPath string,
	prNumber int,
	sourceCommitSHA string,
	sourcePath string,
) {

	// Create upload key — includes CommitStrategy so that workflows with
	// different strategies targeting the same repo produce separate operations.
	key := types.UploadKey{
		RepoName:       workflow.Destination.Repo,
		BranchPath:     workflow.Destination.Branch,
		CommitStrategy: getCommitStrategyType(workflow),
	}

	// Get existing entries from FileStateService
	filesToUpload := wp.fileStateService.GetFilesToUpload()
	content, exists := filesToUpload[key]
	if !exists {
		content = types.UploadFileContent{
			Content:        []github.RepositoryContent{},
			CommitStrategy: types.CommitStrategy(getCommitStrategyType(workflow)),
			UsePRTemplate:  getUsePRTemplate(workflow),
			AutoMergePR:    getAutoMerge(workflow),
		}
	} else {
		// When batching multiple workflows, use AND logic for auto-merge (conservative):
		// auto-merge is only enabled if ALL workflows in the batch want it.
		// Log a warning when workflows have conflicting auto-merge settings.
		workflowAutoMerge := getAutoMerge(workflow)
		if workflowAutoMerge != content.AutoMergePR {
			LogWarning("Workflows in batch have conflicting auto_merge settings; using AND logic (auto-merge disabled)",
				"workflow", workflow.Name,
				"target", key.RepoName,
				"workflow_auto_merge", workflowAutoMerge,
				"batch_auto_merge", content.AutoMergePR,
			)
			// AND logic: if either is false, result is false
			content.AutoMergePR = false
		}
		// For PR template, use OR logic - if any workflow wants it, use it
		if getUsePRTemplate(workflow) && !content.UsePRTemplate {
			content.UsePRTemplate = true
		}
	}

	// Add file to content
	content.Content = append(content.Content, *fileContent)
	content.FileMeta = append(content.FileMeta, types.CopierFileMeta{
		RuleName:   workflow.Name,
		SourceRepo: workflow.Source.Repo,
		SourcePath: sourcePath,
		CommitSHA:  sourceCommitSHA,
		PRNumber:   prNumber,
	})

	// Render templates with message context
	msgCtx := types.NewMessageContext()
	msgCtx.SourceRepo = workflow.Source.Repo
	msgCtx.SourceBranch = workflow.Source.Branch
	msgCtx.TargetRepo = workflow.Destination.Repo
	msgCtx.TargetBranch = workflow.Destination.Branch
	msgCtx.PRNumber = prNumber
	msgCtx.CommitSHA = sourceCommitSHA
	msgCtx.FileCount = len(content.Content)

	// Track previous metadata so we can log when a later workflow overwrites it.
	prevCommitMsg := content.CommitMessage
	prevPRTitle := content.PRTitle

	// Render commit message
	if workflow.CommitStrategy != nil && workflow.CommitStrategy.CommitMessage != "" {
		content.CommitMessage = wp.messageTemplater.RenderCommitMessage(workflow.CommitStrategy.CommitMessage, msgCtx)
	} else {
		content.CommitMessage = fmt.Sprintf("Update from workflow: %s", workflow.Name)
	}

	// Render PR title
	if workflow.CommitStrategy != nil && workflow.CommitStrategy.PRTitle != "" {
		content.PRTitle = wp.messageTemplater.RenderPRTitle(workflow.CommitStrategy.PRTitle, msgCtx)
	} else {
		content.PRTitle = content.CommitMessage
	}

	// Render PR body
	if workflow.CommitStrategy != nil && workflow.CommitStrategy.PRBody != "" {
		content.PRBody = wp.messageTemplater.RenderPRBody(workflow.CommitStrategy.PRBody, msgCtx)
	}

	// Log when a subsequent workflow in the same batch overwrites PR metadata.
	if exists && prevCommitMsg != "" && prevCommitMsg != content.CommitMessage {
		LogInfo("Workflow overwrites batched commit message (last wins)",
			"workflow", workflow.Name,
			"target", workflow.Destination.Repo,
			"prev_commit_message", prevCommitMsg,
			"new_commit_message", content.CommitMessage,
		)
	}
	if exists && prevPRTitle != "" && prevPRTitle != content.PRTitle {
		LogInfo("Workflow overwrites batched PR title (last wins)",
			"workflow", workflow.Name,
			"target", workflow.Destination.Repo,
			"prev_pr_title", prevPRTitle,
			"new_pr_title", content.PRTitle,
		)
	}

	// Add back to FileStateService
	wp.fileStateService.AddFileToUpload(key, content)

	// Record metric (with zero duration since we're just queuing)
	if wp.metricsCollector != nil {
		wp.metricsCollector.RecordFileUploaded(0 * time.Second)
	}
}

// Helper functions to extract config values

func getCommitStrategyType(workflow types.Workflow) string {
	if workflow.CommitStrategy != nil && workflow.CommitStrategy.Type != "" {
		return workflow.CommitStrategy.Type
	}
	return "pull_request" // default
}

func getUsePRTemplate(workflow types.Workflow) bool {
	if workflow.CommitStrategy != nil {
		return workflow.CommitStrategy.UsePRTemplate
	}
	return false
}

func getAutoMerge(workflow types.Workflow) bool {
	if workflow.CommitStrategy != nil {
		return workflow.CommitStrategy.AutoMerge
	}
	return false
}
