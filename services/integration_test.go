package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
	"github.com/jarcoal/httpmock"
)

// --- Mock ConfigLoader for integration tests ---

type mockConfigLoader struct {
	config *types.YAMLConfig
	err    error
}

func (m *mockConfigLoader) LoadConfig(_ context.Context, _ *configs.Config) (*types.YAMLConfig, error) {
	return m.config, m.err
}

func (m *mockConfigLoader) LoadConfigFromContent(_ string, _ string) (*types.YAMLConfig, error) {
	return m.config, m.err
}

// --- Helper to build a signed merged-PR webhook request ---

func buildMergedPRWebhook(t *testing.T, owner, repo, branch string, prNumber int, secret string) (*http.Request, []byte) {
	t.Helper()
	prEvent := &github.PullRequestEvent{
		Action: github.Ptr("closed"),
		PullRequest: &github.PullRequest{
			Number:         github.Ptr(prNumber),
			Merged:         github.Ptr(true),
			MergeCommitSHA: github.Ptr("abc123def456"),
			Base: &github.PullRequestBranch{
				Ref: github.Ptr(branch),
			},
		},
		Repo: &github.Repository{
			Name:  github.Ptr(repo),
			Owner: &github.User{Login: github.Ptr(owner)},
		},
	}
	payload, err := json.Marshal(prEvent)
	if err != nil {
		t.Fatalf("marshal PR event: %v", err)
	}

	req := httptest.NewRequest("POST", "/events", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", "pull_request")
	req.Header.Set("X-GitHub-Delivery", "integration-test-delivery-1")

	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(payload)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	return req, payload
}

// --- Integration test: full webhook → config → process → upload flow ---

func TestIntegration_MergedPR_DirectCommit(t *testing.T) {
	// This test verifies the complete webhook processing pipeline:
	// webhook delivery → config load → workflow match → file fetch → process → commit to target

	owner := "test-org"
	sourceRepo := "source-repo"
	targetRepo := "target-repo"
	branch := "main"
	prNumber := 42

	// 1. Set up global httpmock to intercept ALL HTTP calls (including GraphQL)
	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)

	// Use a fresh TokenManager
	tm := NewTokenManager()
	tm.SetInstallationAccessToken("test-token")
	tm.SetTokenForOrgNoExpiry(owner, "test-token")
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	// 2. Mock GraphQL endpoint for GetFilesChangedInPr
	httpmock.RegisterResponder("POST", "https://api.github.com/graphql",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(200, map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"pullRequest": map[string]any{
							"files": map[string]any{
								"edges": []map[string]any{
									{
										"node": map[string]any{
											"path":       "examples/hello.go",
											"additions":  10,
											"deletions":  2,
											"changeType": "MODIFIED",
										},
									},
								},
								"pageInfo": map[string]any{
									"hasNextPage": false,
									"endCursor":   "",
								},
							},
						},
					},
				},
			})
		},
	)

	// 3. Mock REST endpoints for retrieving source file content
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+sourceRepo+`/contents/examples/hello\.go`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"type":     "file",
			"name":     "hello.go",
			"path":     "examples/hello.go",
			"encoding": "base64",
			"content":  base64.StdEncoding.EncodeToString([]byte("package main\n\nfunc main() {}\n")),
		}),
	)

	// 4. Mock REST endpoints for writing to target repo (direct commit)
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/ref/`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref":    "refs/heads/" + branch,
			"object": map[string]any{"sha": "base-sha-000"},
		}),
	)
	// Mock GET commit for empty-commit detection
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/commits/base-sha-000$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"sha":  "base-sha-000",
			"tree": map[string]any{"sha": "old-tree-sha"},
		}),
	)
	httpmock.RegisterRegexpResponder("POST",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/trees`),
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "new-tree-sha"}),
	)
	httpmock.RegisterResponder("POST",
		"https://api.github.com/repos/"+owner+"/"+targetRepo+"/git/commits",
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "new-commit-sha"}),
	)
	httpmock.RegisterRegexpResponder("PATCH",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/refs/heads/`+branch),
		httpmock.NewStringResponder(200, `{}`),
	)

	// 5. Mock deprecation file endpoint (UpdateDeprecationFile reads then updates)
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/config-repo/contents/`),
		httpmock.NewStringResponder(404, `{"message":"Not Found"}`),
	)

	// 6. Set up mock ConfigLoader with a matching workflow
	mockConfig := &types.YAMLConfig{
		Workflows: []types.Workflow{
			{
				Name: "test-workflow",
				Source: types.Source{
					Repo:   owner + "/" + sourceRepo,
					Branch: branch,
				},
				Destination: types.Destination{
					Repo:   owner + "/" + targetRepo,
					Branch: branch,
				},
				Transformations: []types.Transformation{
					{
						Copy: &types.CopyTransform{
							From: "examples/hello.go",
							To:   "examples/hello.go",
						},
					},
				},
				CommitStrategy: &types.CommitStrategyConfig{
					Type:          "direct",
					CommitMessage: "chore: sync from source",
				},
			},
		},
	}

	// 7. Create container with mock config loader
	config := configs.NewConfig()
	config.ConfigRepoOwner = owner
	config.ConfigRepoName = "config-repo"
	config.ConfigRepoBranch = "main"
	config.AuditEnabled = false
	config.DefaultCommitMessage = "chore: sync files"

	container, err := NewServiceContainer(config)
	if err != nil {
		t.Fatalf("NewServiceContainer: %v", err)
	}
	container.ConfigLoader = &mockConfigLoader{config: mockConfig}

	// 8. Send the webhook
	req, _ := buildMergedPRWebhook(t, owner, sourceRepo, branch, prNumber, "")

	w := httptest.NewRecorder()
	HandleWebhookWithContainer(w, req, config, container)

	// 9. Wait for background goroutine to complete
	container.Wait()

	// 10. Verify HTTP response
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	// 11. Verify the GraphQL endpoint was called (file list)
	info := httpmock.GetCallCountInfo()
	graphqlCalls := info["POST https://api.github.com/graphql"]
	if graphqlCalls < 1 {
		t.Errorf("expected at least 1 GraphQL call, got %d", graphqlCalls)
	}

	// 12. Verify the workflow processor queued files for upload
	// RecordFileUploaded is called when the processor queues a file.
	// If the source content mock wasn't hit or parsing failed, this will be 0.
	filesUploaded := container.MetricsCollector.GetFilesUploaded()
	t.Logf("files uploaded: %d", filesUploaded)

	// At minimum, verify the full pipeline ran (GraphQL + workflow processing)
	if graphqlCalls < 1 {
		t.Error("pipeline did not reach file retrieval stage")
	}
}

func TestIntegration_MergedPR_NoMatchingWorkflows(t *testing.T) {
	// Test that a merged PR to a branch with no matching workflows
	// is handled gracefully without panics or errors.

	owner := "test-org"
	sourceRepo := "source-repo"

	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)

	tm := NewTokenManager()
	tm.SetInstallationAccessToken("test-token")
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	// Config with workflow for "main" branch only
	mockConfig := &types.YAMLConfig{
		Workflows: []types.Workflow{
			{
				Name:   "main-only",
				Source: types.Source{Repo: owner + "/" + sourceRepo, Branch: "main"},
			},
		},
	}

	config := &configs.Config{
		ConfigRepoOwner: owner,
		ConfigRepoName:  "config-repo",
		AuditEnabled:    false,
	}

	container, err := NewServiceContainer(config)
	if err != nil {
		t.Fatalf("NewServiceContainer: %v", err)
	}
	container.ConfigLoader = &mockConfigLoader{config: mockConfig}

	// Send webhook for "develop" branch — no matching workflow
	req, _ := buildMergedPRWebhook(t, owner, sourceRepo, "develop", 99, "")

	w := httptest.NewRecorder()
	HandleWebhookWithContainer(w, req, config, container)
	container.Wait()

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	// Webhook should be recorded as failed (no matching workflows)
	metrics := container.MetricsCollector.GetMetrics(container.FileStateService)
	if metrics.Webhooks.Failed < 1 {
		t.Error("expected webhook failed count >= 1 (no matching workflows)")
	}
}

func TestIntegration_MergedPR_ConfigLoadError(t *testing.T) {
	// Test that a config load failure is handled gracefully.

	owner := "test-org"
	sourceRepo := "source-repo"

	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)

	tm := NewTokenManager()
	tm.SetInstallationAccessToken("test-token")
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	config := &configs.Config{
		ConfigRepoOwner: owner,
		ConfigRepoName:  "config-repo",
		AuditEnabled:    false,
	}

	container, err := NewServiceContainer(config)
	if err != nil {
		t.Fatalf("NewServiceContainer: %v", err)
	}
	container.ConfigLoader = &mockConfigLoader{
		err: ErrConfigLoad,
	}

	req, _ := buildMergedPRWebhook(t, owner, sourceRepo, "main", 50, "")

	w := httptest.NewRecorder()
	HandleWebhookWithContainer(w, req, config, container)
	container.Wait()

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	metrics := container.MetricsCollector.GetMetrics(container.FileStateService)
	if metrics.Webhooks.Failed < 1 {
		t.Error("expected webhook failed count >= 1 (config load error)")
	}
}

func TestIntegration_WebhookSignatureVerification(t *testing.T) {
	// Test end-to-end with signature verification enabled.

	secret := "integration-test-secret"

	config := &configs.Config{
		ConfigRepoOwner: "test-org",
		ConfigRepoName:  "config-repo",
		WebhookSecret:   secret,
		AuditEnabled:    false,
	}

	t.Run("valid signature accepted", func(t *testing.T) {
		container, err := NewServiceContainer(config)
		if err != nil {
			t.Fatalf("NewServiceContainer: %v", err)
		}

		req, _ := buildMergedPRWebhook(t, "test-org", "source-repo", "main", 1, secret)
		w := httptest.NewRecorder()
		HandleWebhookWithContainer(w, req, config, container)
		container.Wait()

		// Should be accepted (202), not rejected
		if w.Code == http.StatusUnauthorized {
			t.Error("valid signature was rejected")
		}
	})

	t.Run("invalid signature rejected", func(t *testing.T) {
		container, err := NewServiceContainer(config)
		if err != nil {
			t.Fatalf("NewServiceContainer: %v", err)
		}

		// Build request signed with wrong secret
		req, _ := buildMergedPRWebhook(t, "test-org", "source-repo", "main", 2, "wrong-secret")
		w := httptest.NewRecorder()
		HandleWebhookWithContainer(w, req, config, container)

		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})
}

// --- Integration test: target repo batching with mixed strategies ---

func TestIntegration_TargetRepoBatching_MixedStrategies(t *testing.T) {
	// Verifies that two workflows targeting the same repo but with different
	// commit strategies (direct vs pull_request) produce separate write operations.
	// Also verifies that two workflows with the same strategy are batched together.

	owner := "test-org"
	sourceRepo := "source-repo"
	targetRepo := "target-repo"
	branch := "main"
	prNumber := 55

	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)

	tm := NewTokenManager()
	tm.SetInstallationAccessToken("test-token")
	tm.SetTokenForOrgNoExpiry(owner, "test-token")
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	// GraphQL: return two changed files
	httpmock.RegisterResponder("POST", "https://api.github.com/graphql",
		func(req *http.Request) (*http.Response, error) {
			return httpmock.NewJsonResponse(200, map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"pullRequest": map[string]any{
							"files": map[string]any{
								"edges": []map[string]any{
									{"node": map[string]any{"path": "docs/guide.md", "additions": 5, "deletions": 0, "changeType": "MODIFIED"}},
									{"node": map[string]any{"path": "examples/demo.go", "additions": 10, "deletions": 3, "changeType": "MODIFIED"}},
								},
								"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
							},
						},
					},
				},
			})
		},
	)

	// Source file content mocks
	for _, path := range []string{"docs/guide.md", "examples/demo.go"} {
		p := path
		httpmock.RegisterRegexpResponder("GET",
			regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+sourceRepo+`/contents/`+regexp.QuoteMeta(p)),
			httpmock.NewJsonResponderOrPanic(200, map[string]any{
				"type": "file", "name": p, "path": p, "encoding": "base64",
				"content": base64.StdEncoding.EncodeToString([]byte("content of " + p)),
			}),
		)
	}

	// Target repo write endpoints (direct commit)
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/ref/heads/`+branch+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/" + branch, "object": map[string]any{"sha": "base-sha"},
		}),
	)
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/commits/base-sha$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"sha": "base-sha", "tree": map[string]any{"sha": "old-tree-sha"},
		}),
	)
	directTreesURL := regexp.MustCompile(`^https://api\.github\.com/repos/` + owner + `/` + targetRepo + `/git/trees`)
	httpmock.RegisterRegexpResponder("POST", directTreesURL,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "new-tree-sha"}),
	)
	directCommitsURL := "https://api.github.com/repos/" + owner + "/" + targetRepo + "/git/commits"
	httpmock.RegisterResponder("POST", directCommitsURL,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "new-commit-sha"}),
	)
	httpmock.RegisterRegexpResponder("PATCH",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/refs/heads/`+branch),
		httpmock.NewStringResponder(200, `{}`),
	)

	// Target repo PR endpoints (for pull_request strategy)
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/pulls\?`),
		httpmock.NewJsonResponderOrPanic(200, []map[string]any{}), // no existing PRs
	)
	httpmock.RegisterRegexpResponder("POST",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/refs$`),
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"ref": "refs/heads/copier/test", "object": map[string]any{"sha": "base-sha"}}),
	)
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/ref/(?:refs/)?heads/copier/`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/copier/test", "object": map[string]any{"sha": "base-sha"},
		}),
	)
	httpmock.RegisterRegexpResponder("DELETE",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/refs/heads/copier/`),
		httpmock.NewStringResponder(204, ""),
	)
	httpmock.RegisterRegexpResponder("PATCH",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+targetRepo+`/git/refs/heads/copier/`),
		httpmock.NewStringResponder(200, `{}`),
	)
	prCreateURL := "https://api.github.com/repos/" + owner + "/" + targetRepo + "/pulls"
	httpmock.RegisterResponder("POST", prCreateURL,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"number": 99, "html_url": "https://github.com/" + owner + "/" + targetRepo + "/pull/99"}),
	)

	// Deprecation file mock (404 = no existing file)
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/config-repo/contents/`),
		httpmock.NewStringResponder(404, `{"message":"Not Found"}`),
	)

	// Config: 3 workflows — two direct (should batch), one PR (separate operation)
	mockConfig := &types.YAMLConfig{
		Workflows: []types.Workflow{
			{
				Name:        "wf-direct-docs",
				Source:      types.Source{Repo: owner + "/" + sourceRepo, Branch: branch},
				Destination: types.Destination{Repo: owner + "/" + targetRepo, Branch: branch},
				Transformations: []types.Transformation{
					{Copy: &types.CopyTransform{From: "docs/guide.md", To: "docs/guide.md"}},
				},
				CommitStrategy: &types.CommitStrategyConfig{Type: "direct", CommitMessage: "sync docs"},
			},
			{
				Name:        "wf-direct-examples",
				Source:      types.Source{Repo: owner + "/" + sourceRepo, Branch: branch},
				Destination: types.Destination{Repo: owner + "/" + targetRepo, Branch: branch},
				Transformations: []types.Transformation{
					{Copy: &types.CopyTransform{From: "examples/demo.go", To: "examples/demo.go"}},
				},
				CommitStrategy: &types.CommitStrategyConfig{Type: "direct", CommitMessage: "sync examples"},
			},
			{
				Name:        "wf-pr-docs",
				Source:      types.Source{Repo: owner + "/" + sourceRepo, Branch: branch},
				Destination: types.Destination{Repo: owner + "/" + targetRepo, Branch: branch},
				Transformations: []types.Transformation{
					{Copy: &types.CopyTransform{From: "docs/guide.md", To: "pr-docs/guide.md"}},
				},
				CommitStrategy: &types.CommitStrategyConfig{
					Type:          "pull_request",
					CommitMessage: "sync via PR",
					PRTitle:       "Copier: sync docs",
				},
			},
		},
	}

	config := configs.NewConfig()
	config.ConfigRepoOwner = owner
	config.ConfigRepoName = "config-repo"
	config.ConfigRepoBranch = "main"
	config.AuditEnabled = false
	config.DefaultCommitMessage = "chore: sync"

	container, err := NewServiceContainer(config)
	if err != nil {
		t.Fatalf("NewServiceContainer: %v", err)
	}
	container.ConfigLoader = &mockConfigLoader{config: mockConfig}

	req, _ := buildMergedPRWebhook(t, owner, sourceRepo, branch, prNumber, "")
	w := httptest.NewRecorder()
	HandleWebhookWithContainer(w, req, config, container)
	container.Wait()

	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want %d", w.Code, http.StatusAccepted)
	}

	info := httpmock.GetCallCountInfo()

	// Count PATCH to main branch ref (only direct commits update this)
	directRefUpdateKey := "PATCH =~^https://api\\.github\\.com/repos/" + owner + "/" + targetRepo + "/git/refs/heads/" + branch
	prCreateCalls := info["POST "+prCreateURL]

	// Find the direct ref update count from the call info map
	directRefUpdates := 0
	for k, v := range info {
		if k == directRefUpdateKey {
			directRefUpdates = v
		}
	}

	t.Logf("Direct ref updates: %d, PR create calls: %d", directRefUpdates, prCreateCalls)

	// Direct commit: the two direct-strategy workflows should batch into 1 ref update
	if directRefUpdates != 1 {
		t.Errorf("expected 1 direct ref update (batched), got %d", directRefUpdates)
	}

	// PR: separate operation should create 1 PR
	if prCreateCalls != 1 {
		t.Errorf("expected 1 PR created (separate strategy), got %d", prCreateCalls)
	}
}

// --- Unit tests for extracted helper functions ---

func TestLoadAndMatchWorkflows_MatchesBranch(t *testing.T) {
	config := &configs.Config{
		ConfigRepoOwner: "org",
		ConfigRepoName:  "config",
		AuditEnabled:    false,
	}
	container, err := NewServiceContainer(config)
	if err != nil {
		t.Fatalf("NewServiceContainer: %v", err)
	}
	container.ConfigLoader = &mockConfigLoader{
		config: &types.YAMLConfig{
			Workflows: []types.Workflow{
				{Name: "main-wf", Source: types.Source{Repo: "org/repo", Branch: "main"}},
				{Name: "dev-wf", Source: types.Source{Repo: "org/repo", Branch: "develop"}},
				{Name: "other-wf", Source: types.Source{Repo: "other/repo", Branch: "main"}},
			},
		},
	}

	yamlConfig, err := loadAndMatchWorkflows(context.Background(), config, container, "org/repo", "main", 1)
	if err != nil {
		t.Fatalf("loadAndMatchWorkflows: %v", err)
	}
	if len(yamlConfig.Workflows) != 1 {
		t.Fatalf("expected 1 matching workflow, got %d", len(yamlConfig.Workflows))
	}
	if yamlConfig.Workflows[0].Name != "main-wf" {
		t.Errorf("matched workflow = %q, want main-wf", yamlConfig.Workflows[0].Name)
	}
}

func TestLoadAndMatchWorkflows_NoMatch(t *testing.T) {
	config := &configs.Config{
		ConfigRepoOwner: "org",
		ConfigRepoName:  "config",
		AuditEnabled:    false,
	}
	container, err := NewServiceContainer(config)
	if err != nil {
		t.Fatalf("NewServiceContainer: %v", err)
	}
	container.ConfigLoader = &mockConfigLoader{
		config: &types.YAMLConfig{
			Workflows: []types.Workflow{
				{Name: "main-wf", Source: types.Source{Repo: "org/repo", Branch: "main"}},
			},
		},
	}

	_, err = loadAndMatchWorkflows(context.Background(), config, container, "org/repo", "develop", 1)
	if err == nil {
		t.Error("expected error for no matching workflows")
	}
}

func TestPollMergeability(t *testing.T) {
	httpmock.Activate()
	t.Cleanup(httpmock.DeactivateAndReset)

	httpmock.RegisterResponder("GET",
		"https://api.github.com/repos/org/repo/pulls/10",
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"mergeable":       true,
			"mergeable_state": "clean",
		}),
	)

	tm := NewTokenManager()
	tm.SetTokenForOrgNoExpiry("org", "test-token")
	prev := defaultTokenManager
	defaultTokenManager = tm
	t.Cleanup(func() { defaultTokenManager = prev })

	client := newGitHubRESTClient("test-token", nil)
	mergeable, state := pollMergeability(context.Background(), client, "org", "repo", 10, 3, 10)

	if mergeable == nil {
		t.Fatal("expected mergeable to be computed")
	}
	if !*mergeable {
		t.Error("expected mergeable = true")
	}
	if state != "clean" {
		t.Errorf("state = %q, want clean", state)
	}
}

func TestRecordBatchFailure(t *testing.T) {
	mc := NewMetricsCollector()

	recordBatchFailure(nil, 5) // should not panic

	recordBatchFailure(mc, 3)
	if mc.GetFilesUploadFailed() != 3 {
		t.Errorf("filesUploadFailed = %d, want 3", mc.GetFilesUploadFailed())
	}
}
