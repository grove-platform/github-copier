package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
)

const (
	maxWebhookBodyBytes = 1 << 20 // 1MB
	// GitHub GraphQL API returns file status in uppercase for the ChangeType field
	// Possible values: ADDED, MODIFIED, DELETED, RENAMED, COPIED, CHANGED
	statusDeleted = "DELETED"
)

// simpleVerifySignature verifies the webhook signature
func simpleVerifySignature(sigHeader string, body, secret []byte) bool {
	if sigHeader == "" {
		return false
	}

	// Remove "sha256=" prefix
	if !strings.HasPrefix(sigHeader, "sha256=") {
		return false
	}
	signature := sigHeader[7:]

	// Compute HMAC
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedMAC))
}

// RetrieveFileContentsWithConfigAndBranch fetches file contents from a specific branch
func RetrieveFileContentsWithConfigAndBranch(ctx context.Context, config *configs.Config, filePath string, branch string, repoOwner string, repoName string) (*github.RepositoryContent, error) {
	// Use org-specific client to ensure we have the right installation token
	client, err := GetRestClientForOrg(ctx, config, repoOwner)
	if err != nil {
		return nil, fmt.Errorf("failed to get GitHub client for org %s: %w", repoOwner, err)
	}

	fileContent, _, _, err := client.Repositories.GetContents(
		ctx,
		repoOwner,
		repoName,
		filePath,
		&github.RepositoryContentGetOptions{
			Ref: branch,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get file content: %w", err)
	}

	return fileContent, nil
}

// HandleWebhookWithContainer handles incoming GitHub webhook requests using the service container
func HandleWebhookWithContainer(w http.ResponseWriter, r *http.Request, config *configs.Config, container *ServiceContainer) {
	// GitHub webhooks are always POST
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	startTime := time.Now()
	ctx := r.Context()

	LogInfoCtx(ctx, "webhook handler started", map[string]interface{}{
		"elapsed_ms": time.Since(startTime).Milliseconds(),
	})

	// Read and validate webhook payload
	limited := io.LimitReader(r.Body, maxWebhookBodyBytes)
	payload, err := io.ReadAll(limited)
	if err != nil {
		LogWebhookOperation(ctx, "read_body", "failed to read webhook body", err)
		container.MetricsCollector.RecordWebhookFailed()
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	if eventType == "" {
		LogWebhookOperation(ctx, "missing_event", "missing X-GitHub-Event header", nil)
		container.MetricsCollector.RecordWebhookFailed()
		http.Error(w, "missing event type", http.StatusBadRequest)
		return
	}

	// Check for duplicate delivery using X-GitHub-Delivery header
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID != "" && container.DeliveryTracker != nil {
		if !container.DeliveryTracker.TryRecord(deliveryID) {
			LogInfoCtx(ctx, "duplicate webhook delivery, skipping", map[string]interface{}{
				"delivery_id": deliveryID,
				"event_type":  eventType,
			})
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	LogInfoCtx(ctx, "payload read", map[string]interface{}{
		"elapsed_ms":  time.Since(startTime).Milliseconds(),
		"size_bytes":  len(payload),
		"delivery_id": deliveryID,
	})

	// Verify webhook signature
	if config.WebhookSecret != "" {
		sigHeader := r.Header.Get("X-Hub-Signature-256")
		if !simpleVerifySignature(sigHeader, payload, []byte(config.WebhookSecret)) {
			LogWebhookOperation(ctx, "signature_verification", "webhook signature verification failed", nil)
			container.MetricsCollector.RecordWebhookFailed()
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		LogInfoCtx(ctx, "signature verified", map[string]interface{}{
			"elapsed_ms": time.Since(startTime).Milliseconds(),
		})
	} else {
		LogWarningCtx(ctx, "webhook signature verification DISABLED - no webhook secret configured; set WEBHOOK_SECRET for production use", nil)
	}

	// Parse webhook event
	evt, err := github.ParseWebHook(eventType, payload)
	if err != nil {
		LogWebhookOperation(ctx, "parse_payload", "failed to parse webhook payload", err,
			map[string]interface{}{"event_type": eventType})
		container.MetricsCollector.RecordWebhookFailed()
		http.Error(w, "bad webhook", http.StatusBadRequest)
		return
	}

	// Check if it's a pull_request event
	prEvt, ok := evt.(*github.PullRequestEvent)
	if !ok || prEvt.GetPullRequest() == nil {
		// Record ignored webhook with event type
		container.MetricsCollector.RecordWebhookIgnored(eventType)

		// Log with event type for better debugging
		LogInfoCtx(ctx, "ignoring non-pull_request event", map[string]interface{}{
			"event_type": eventType,
			"size_bytes": len(payload),
		})
		w.WriteHeader(http.StatusNoContent)
		return
	}

	action := prEvt.GetAction()
	merged := prEvt.GetPullRequest().GetMerged()

	LogInfoCtx(ctx, "PR event received", map[string]interface{}{
		"action": action,
		"merged": merged,
	})

	if !(action == "closed" && merged) {
		LogInfoCtx(ctx, "skipping non-merged PR", map[string]interface{}{
			"action": action,
			"merged": merged,
		})
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Process the merged PR
	prNumber := prEvt.GetPullRequest().GetNumber()
	sourceCommitSHA := prEvt.GetPullRequest().GetMergeCommitSHA()

	// Extract repository info from webhook payload
	repo := prEvt.GetRepo()
	if repo == nil {
		LogWarningCtx(ctx, "webhook missing repository info", nil)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	repoOwner := repo.GetOwner().GetLogin()
	repoName := repo.GetName()

	// Extract the base branch (the branch the PR was merged into)
	baseBranch := prEvt.GetPullRequest().GetBase().GetRef()

	LogInfoCtx(ctx, "processing merged PR", map[string]interface{}{
		"pr_number":   prNumber,
		"sha":         sourceCommitSHA,
		"repo":        fmt.Sprintf("%s/%s", repoOwner, repoName),
		"base_branch": baseBranch,
		"delivery_id": deliveryID,
		"elapsed_ms":  time.Since(startTime).Milliseconds(),
	})

	// Respond immediately to avoid GitHub webhook timeout
	LogInfoCtx(ctx, "sending immediate response", map[string]interface{}{
		"elapsed_ms": time.Since(startTime).Milliseconds(),
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if _, err := w.Write([]byte(`{"status":"accepted"}`)); err != nil {
		LogWarningCtx(ctx, "failed to write webhook response body", map[string]interface{}{"error": err.Error()})
	}

	LogInfoCtx(ctx, "response sent", map[string]interface{}{
		"elapsed_ms": time.Since(startTime).Milliseconds(),
	})

	// Flush the response immediately
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
		LogInfoCtx(ctx, "response flushed", map[string]interface{}{
			"elapsed_ms": time.Since(startTime).Milliseconds(),
		})
	}

	// Process asynchronously in background with a new context
	// Don't use the request context as it will be cancelled when the request completes
	bgCtx := context.Background()
	container.wg.Add(1)
	go func() {
		defer container.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				LogCritical("panic in webhook handler", "pr_number", prNumber, "repo_owner", repoOwner, "repo_name", repoName, "recovered", r)
				container.MetricsCollector.RecordWebhookFailed()
				if notifyErr := container.SlackNotifier.NotifyError(bgCtx, &ErrorEvent{
					Operation:  "panic_recovery",
					Error:      fmt.Errorf("panic: %v", r),
					PRNumber:   prNumber,
					SourceRepo: fmt.Sprintf("%s/%s", repoOwner, repoName),
				}); notifyErr != nil {
					LogWarning("failed to send Slack error notification", "error", notifyErr)
				}
			}
		}()
		handleMergedPRWithContainer(bgCtx, prNumber, sourceCommitSHA, repoOwner, repoName, baseBranch, config, container)
	}()
}

// handleMergedPRWithContainer orchestrates processing of a merged PR:
// auth → config → match workflows → fetch changed files → process → upload → notify.
func handleMergedPRWithContainer(ctx context.Context, prNumber int, sourceCommitSHA string, repoOwner string, repoName string, baseBranch string, config *configs.Config, container *ServiceContainer) {
	startTime := time.Now()
	webhookRepo := fmt.Sprintf("%s/%s", repoOwner, repoName)

	// 1. Ensure GitHub auth
	if defaultTokenManager.GetInstallationAccessToken() == "" {
		if err := ConfigurePermissions(ctx, config); err != nil {
			LogAndReturnError(ctx, "auth", "failed to configure GitHub permissions", err)
			container.MetricsCollector.RecordWebhookFailed()
			return
		}
	}

	// 2. Load config and find matching workflows
	yamlConfig, err := loadAndMatchWorkflows(ctx, config, container, webhookRepo, baseBranch, prNumber)
	if err != nil {
		return // already logged and notified
	}

	// 3. Fetch changed files from the source PR
	changedFiles, err := fetchChangedFiles(ctx, config, container, repoOwner, repoName, prNumber, webhookRepo)
	if err != nil {
		return // already logged and notified
	}

	// 4. Snapshot metrics before processing
	filesMatchedBefore := container.MetricsCollector.GetFilesMatched()
	filesUploadedBefore := container.MetricsCollector.GetFilesUploaded()
	filesFailedBefore := container.MetricsCollector.GetFilesUploadFailed()

	// 5. Process workflows, upload files, and update deprecations
	processFilesWithWorkflows(ctx, prNumber, sourceCommitSHA, changedFiles, yamlConfig, config, container)
	uploadAndDeprecateFiles(ctx, config, container)

	// 6. Report completion
	reportCompletion(ctx, container, webhookRepo, prNumber, sourceCommitSHA, startTime,
		filesMatchedBefore, filesUploadedBefore, filesFailedBefore)
}

// loadAndMatchWorkflows loads the YAML config and filters to workflows matching
// the webhook's source repo and branch. Returns nil and logs/notifies on error.
func loadAndMatchWorkflows(ctx context.Context, config *configs.Config, container *ServiceContainer, webhookRepo string, baseBranch string, prNumber int) (*types.YAMLConfig, error) {
	yamlConfig, err := container.ConfigLoader.LoadConfig(ctx, config)
	if err != nil {
		LogAndReturnError(ctx, "config_load", "failed to load config", err)
		container.MetricsCollector.RecordWebhookFailed()
		notifySlackError(ctx, container, "config_load", err, prNumber, webhookRepo)
		return nil, err
	}

	var matching []types.Workflow
	for _, wf := range yamlConfig.Workflows {
		if wf.Source.Repo == webhookRepo && wf.Source.Branch == baseBranch {
			matching = append(matching, wf)
		}
	}

	if len(matching) == 0 {
		LogWarningCtx(ctx, "no workflows configured for source repository and branch", map[string]interface{}{
			"webhook_repo":   webhookRepo,
			"base_branch":    baseBranch,
			"workflow_count": len(yamlConfig.Workflows),
		})
		container.MetricsCollector.RecordWebhookFailed()
		return nil, fmt.Errorf("no matching workflows")
	}

	LogInfoCtx(ctx, "found matching workflows", map[string]interface{}{
		"webhook_repo":   webhookRepo,
		"base_branch":    baseBranch,
		"matching_count": len(matching),
	})

	yamlConfig.Workflows = matching
	return yamlConfig, nil
}

// fetchChangedFiles retrieves the files changed in a PR, logging and notifying on error.
func fetchChangedFiles(ctx context.Context, config *configs.Config, container *ServiceContainer, repoOwner string, repoName string, prNumber int, webhookRepo string) ([]types.ChangedFile, error) {
	changedFiles, err := GetFilesChangedInPr(ctx, config, repoOwner, repoName, prNumber)
	if err != nil {
		LogAndReturnError(ctx, "get_files", "failed to get changed files", err)
		container.MetricsCollector.RecordWebhookFailed()
		notifySlackError(ctx, container, "get_files", err, prNumber, webhookRepo)
		return nil, err
	}

	LogInfoCtx(ctx, "retrieved changed files", map[string]interface{}{
		"count": len(changedFiles),
	})
	return changedFiles, nil
}

// uploadAndDeprecateFiles drains the file-state queues, uploading files to target
// repos and updating the deprecation file.
func uploadAndDeprecateFiles(ctx context.Context, config *configs.Config, container *ServiceContainer) {
	// Upload queued files
	filesToUpload := container.FileStateService.GetFilesToUpload()
	AddFilesToTargetRepos(ctx, config, filesToUpload, container.PRTemplateFetcher, container.MetricsCollector)
	container.FileStateService.ClearFilesToUpload()

	// Build deprecation map and update file
	deprecationMap := container.FileStateService.GetFilesToDeprecate()
	filesToDeprecate := make(map[string]types.Configs)
	for _, entries := range deprecationMap {
		for _, entry := range entries {
			filesToDeprecate[entry.FileName] = types.Configs{
				TargetRepo:   entry.Repo,
				TargetBranch: entry.Branch,
			}
		}
	}
	UpdateDeprecationFile(ctx, config, filesToDeprecate)
	container.FileStateService.ClearFilesToDeprecate()
}

// reportCompletion calculates processing metrics and sends a Slack notification.
func reportCompletion(ctx context.Context, container *ServiceContainer, webhookRepo string, prNumber int, sourceCommitSHA string, startTime time.Time, matchedBefore int, uploadedBefore int, failedBefore int) {
	filesMatched := container.MetricsCollector.GetFilesMatched() - matchedBefore
	filesUploaded := container.MetricsCollector.GetFilesUploaded() - uploadedBefore
	filesFailed := container.MetricsCollector.GetFilesUploadFailed() - failedBefore
	processingTime := time.Since(startTime)

	LogInfoCtx(ctx, "--Done--", map[string]interface{}{
		"pr_number": prNumber,
		"sha":       sourceCommitSHA,
	})

	if notifyErr := container.SlackNotifier.NotifyPRProcessed(ctx, &PRProcessedEvent{
		PRNumber:       prNumber,
		PRTitle:        fmt.Sprintf("PR #%d", prNumber),
		PRURL:          fmt.Sprintf("https://github.com/%s/pull/%d", webhookRepo, prNumber),
		SourceRepo:     webhookRepo,
		FilesMatched:   filesMatched,
		FilesCopied:    filesUploaded,
		FilesFailed:    filesFailed,
		ProcessingTime: processingTime,
	}); notifyErr != nil {
		LogWarningCtx(ctx, "failed to send Slack PR processed notification", map[string]interface{}{"error": notifyErr.Error()})
	}
}

// notifySlackError is a helper to send a Slack error notification, logging any failure.
func notifySlackError(ctx context.Context, container *ServiceContainer, operation string, err error, prNumber int, sourceRepo string) {
	if notifyErr := container.SlackNotifier.NotifyError(ctx, &ErrorEvent{
		Operation:  operation,
		Error:      err,
		PRNumber:   prNumber,
		SourceRepo: sourceRepo,
	}); notifyErr != nil {
		LogWarningCtx(ctx, "failed to send Slack error notification", map[string]interface{}{"error": notifyErr.Error()})
	}
}

// processFilesWithWorkflows processes changed files using the workflow system
func processFilesWithWorkflows(ctx context.Context, prNumber int, sourceCommitSHA string,
	changedFiles []types.ChangedFile, yamlConfig *types.YAMLConfig, config *configs.Config, container *ServiceContainer) {

	LogInfoCtx(ctx, "processing files with workflows", map[string]interface{}{
		"file_count":     len(changedFiles),
		"workflow_count": len(yamlConfig.Workflows),
	})

	// Create workflow processor
	workflowProcessor := NewWorkflowProcessor(
		container.PatternMatcher,
		container.PathTransformer,
		container.FileStateService,
		container.MetricsCollector,
		container.MessageTemplater,
		config,
	)

	// Process each workflow
	for _, workflow := range yamlConfig.Workflows {
		if err := ctx.Err(); err != nil {
			LogWebhookOperation(ctx, "workflow_processing", "workflow processing cancelled", err)
			return
		}

		err := workflowProcessor.ProcessWorkflow(ctx, workflow, changedFiles, prNumber, sourceCommitSHA)
		if err != nil {
			LogErrorCtx(ctx, "failed to process workflow", err, map[string]interface{}{
				"workflow_name": workflow.Name,
			})
			// Continue processing other workflows
			continue
		}
	}

	LogInfoCtx(ctx, "workflow processing complete", map[string]interface{}{
		"workflow_count": len(yamlConfig.Workflows),
	})
}
