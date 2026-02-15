package services_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/services"
	"github.com/grove-platform/github-copier/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetricsCollector_WebhookMetrics(t *testing.T) {
	collector := services.NewMetricsCollector()

	// Record some webhooks
	collector.RecordWebhookReceived()
	collector.RecordWebhookReceived()
	collector.RecordWebhookReceived()

	collector.RecordWebhookProcessed(100 * time.Millisecond)
	collector.RecordWebhookProcessed(200 * time.Millisecond)

	collector.RecordWebhookFailed()

	// Get metrics
	fileStateService := services.NewFileStateService()
	metrics := collector.GetMetrics(fileStateService)

	assert.Equal(t, int64(3), metrics.Webhooks.Received)
	assert.Equal(t, int64(2), metrics.Webhooks.Processed)
	assert.Equal(t, int64(1), metrics.Webhooks.Failed)
	assert.InDelta(t, 66.67, metrics.Webhooks.SuccessRate, 0.1)
}

func TestMetricsCollector_FileMetrics(t *testing.T) {
	collector := services.NewMetricsCollector()

	// Record file operations
	collector.RecordFileMatched()
	collector.RecordFileMatched()
	collector.RecordFileMatched()

	collector.RecordFileUploaded(50 * time.Millisecond)
	collector.RecordFileUploaded(100 * time.Millisecond)

	collector.RecordFileUploadFailed()

	collector.RecordFileDeprecated()

	// Get metrics
	fileStateService := services.NewFileStateService()
	metrics := collector.GetMetrics(fileStateService)

	assert.Equal(t, int64(3), metrics.Files.Matched)
	assert.Equal(t, int64(2), metrics.Files.Uploaded)
	assert.Equal(t, int64(1), metrics.Files.UploadFailed)
	assert.Equal(t, int64(1), metrics.Files.Deprecated)
	assert.InDelta(t, 66.67, metrics.Files.UploadSuccessRate, 0.1)
}

func TestMetricsCollector_GitHubAPIMetrics(t *testing.T) {
	collector := services.NewMetricsCollector()

	// Record API calls
	collector.RecordGitHubAPICall()
	collector.RecordGitHubAPICall()
	collector.RecordGitHubAPICall()

	collector.RecordGitHubAPIError()

	// Get metrics
	fileStateService := services.NewFileStateService()
	metrics := collector.GetMetrics(fileStateService)

	assert.Equal(t, int64(3), metrics.GitHubAPI.Calls)
	assert.Equal(t, int64(1), metrics.GitHubAPI.Errors)
	// Error rate = errors / (calls + errors) = 1 / 4 = 25%
	assert.InDelta(t, 33.33, metrics.GitHubAPI.ErrorRate, 0.1)
}

func TestMetricsCollector_ProcessingTimePercentiles(t *testing.T) {
	collector := services.NewMetricsCollector()

	// Record processing times
	times := []time.Duration{
		10 * time.Millisecond,
		20 * time.Millisecond,
		30 * time.Millisecond,
		40 * time.Millisecond,
		50 * time.Millisecond,
		60 * time.Millisecond,
		70 * time.Millisecond,
		80 * time.Millisecond,
		90 * time.Millisecond,
		100 * time.Millisecond,
	}

	for _, d := range times {
		collector.RecordWebhookProcessed(d)
	}

	// Get metrics
	fileStateService := services.NewFileStateService()
	metrics := collector.GetMetrics(fileStateService)

	// Check percentiles are reasonable
	assert.Greater(t, metrics.Webhooks.ProcessingTime.P50Ms, float64(0))
	assert.Greater(t, metrics.Webhooks.ProcessingTime.P95Ms, metrics.Webhooks.ProcessingTime.P50Ms)
	assert.Greater(t, metrics.Webhooks.ProcessingTime.P99Ms, metrics.Webhooks.ProcessingTime.P95Ms)
}

func TestMetricsCollector_QueueSizes(t *testing.T) {
	collector := services.NewMetricsCollector()
	fileStateService := services.NewFileStateService()

	// Add some files to queues
	fileStateService.AddFileToUpload(
		types.UploadKey{RepoName: "org/repo", BranchPath: "refs/heads/main"},
		types.UploadFileContent{TargetBranch: "main"},
	)

	fileStateService.AddFileToDeprecate(
		"deprecated.json",
		types.DeprecatedFileEntry{FileName: "test.go"},
	)

	// Get metrics
	metrics := collector.GetMetrics(fileStateService)

	assert.Equal(t, 1, metrics.Queues.UploadQueueSize)
	assert.Equal(t, 1, metrics.Queues.DeprecationQueueSize)
}

func TestHealthHandler(t *testing.T) {
	startTime := time.Now().Add(-1 * time.Hour)

	handler := services.HealthHandler(startTime, "v0.0.0-test")

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var health map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &health)
	require.NoError(t, err)

	assert.Equal(t, "healthy", health["status"])
	assert.True(t, health["started"].(bool))
	assert.NotNil(t, health["uptime"])
}

func TestReadinessHandler(t *testing.T) {
	config := &configs.Config{
		ConfigRepoOwner: "test-owner",
		ConfigRepoName:  "test-repo",
		AuditEnabled:    false,
	}

	container, err := services.NewServiceContainer(config)
	require.NoError(t, err)

	// Clear any token set by previous tests so GitHub shows as not_authenticated
	container.TokenManager.SetInstallationAccessToken("")

	handler := services.ReadinessHandler(container)

	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var health services.HealthStatus
	err = json.Unmarshal(w.Body.Bytes(), &health)
	require.NoError(t, err)

	// With no installation token set, should be not_ready
	assert.Equal(t, "not_ready", health.Status)
	assert.False(t, health.GitHub.Authenticated)
	assert.Equal(t, "not_authenticated", health.GitHub.Status)
	assert.True(t, health.Started)
}

func TestReadinessHandler_WithAuth(t *testing.T) {
	config := &configs.Config{
		ConfigRepoOwner: "test-owner",
		ConfigRepoName:  "test-repo",
		AuditEnabled:    false,
	}

	container, err := services.NewServiceContainer(config)
	require.NoError(t, err)

	// Set a token so GitHub shows as authenticated
	container.TokenManager.SetInstallationAccessToken("test-token")

	handler := services.ReadinessHandler(container)

	req := httptest.NewRequest("GET", "/ready", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var health services.HealthStatus
	err = json.Unmarshal(w.Body.Bytes(), &health)
	require.NoError(t, err)

	assert.Equal(t, "ready", health.Status)
	assert.True(t, health.GitHub.Authenticated)
	assert.Equal(t, "healthy", health.GitHub.Status)
}

func TestMetricsHandler(t *testing.T) {
	collector := services.NewMetricsCollector()
	fileStateService := services.NewFileStateService()

	// Record some metrics
	collector.RecordWebhookReceived()
	collector.RecordWebhookProcessed(100 * time.Millisecond)
	collector.RecordFileMatched()
	collector.RecordFileUploaded(50 * time.Millisecond)

	handler := services.MetricsHandler(collector, fileStateService)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var metrics map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &metrics)
	require.NoError(t, err)

	webhooks := metrics["webhooks"].(map[string]interface{})
	files := metrics["files"].(map[string]interface{})

	assert.Equal(t, float64(1), webhooks["received"])
	assert.Equal(t, float64(1), webhooks["processed"])
	assert.Equal(t, float64(1), files["matched"])
	assert.Equal(t, float64(1), files["uploaded"])
}

func TestMetricsCollector_CircularBuffer(t *testing.T) {
	collector := services.NewMetricsCollector()

	// Record more than buffer size (1000) processing times
	for i := 0; i < 1500; i++ {
		collector.RecordWebhookProcessed(time.Duration(i) * time.Millisecond)
	}

	fileStateService := services.NewFileStateService()
	metrics := collector.GetMetrics(fileStateService)

	// Should still work and not crash
	assert.Greater(t, metrics.Webhooks.ProcessingTime.P50Ms, float64(0))
	assert.Greater(t, metrics.Webhooks.ProcessingTime.P95Ms, float64(0))
	assert.Greater(t, metrics.Webhooks.ProcessingTime.P99Ms, float64(0))
}

func TestMetricsCollector_ZeroValues(t *testing.T) {
	collector := services.NewMetricsCollector()
	fileStateService := services.NewFileStateService()

	// Get metrics without recording anything
	metrics := collector.GetMetrics(fileStateService)

	assert.Equal(t, int64(0), metrics.Webhooks.Received)
	assert.Equal(t, int64(0), metrics.Webhooks.Processed)
	assert.Equal(t, int64(0), metrics.Webhooks.Failed)
	assert.Equal(t, float64(0), metrics.Webhooks.SuccessRate)

	assert.Equal(t, int64(0), metrics.Files.Matched)
	assert.Equal(t, int64(0), metrics.Files.Uploaded)
	assert.Equal(t, int64(0), metrics.Files.UploadFailed)
	assert.Equal(t, float64(0), metrics.Files.UploadSuccessRate)
}

func TestMetricsCollector_SuccessRateCalculation(t *testing.T) {
	tests := []struct {
		name      string
		received  int
		processed int
		failed    int
		wantRate  float64
	}{
		{
			name:      "all success",
			received:  10,
			processed: 10,
			failed:    0,
			wantRate:  100.0,
		},
		{
			name:      "all failed",
			received:  10,
			processed: 0,
			failed:    10,
			wantRate:  0.0,
		},
		{
			name:      "half success",
			received:  10,
			processed: 5,
			failed:    5,
			wantRate:  50.0,
		},
		{
			name:      "no operations",
			received:  0,
			processed: 0,
			failed:    0,
			wantRate:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := services.NewMetricsCollector()

			for i := 0; i < tt.received; i++ {
				collector.RecordWebhookReceived()
			}
			for i := 0; i < tt.processed; i++ {
				collector.RecordWebhookProcessed(10 * time.Millisecond)
			}
			for i := 0; i < tt.failed; i++ {
				collector.RecordWebhookFailed()
			}

			fileStateService := services.NewFileStateService()
			metrics := collector.GetMetrics(fileStateService)

			assert.InDelta(t, tt.wantRate, metrics.Webhooks.SuccessRate, 0.1)
		})
	}
}

func TestConfigDiagnosticHandler_EnvironmentFields(t *testing.T) {
	config := &configs.Config{
		Port:                            "8080",
		DryRun:                          true,
		UseMainConfig:                   true,
		MainConfigFile:                  ".copier/main.yaml",
		ConfigFile:                      "copier-config.yaml",
		ConfigRepoOwner:                 "test-owner",
		ConfigRepoName:                  "test-repo",
		ConfigRepoBranch:                "main",
		WebserverPath:                   "/events",
		AuditEnabled:                    false,
		MetricsEnabled:                  true,
		SlackEnabled:                    true,
		SlackWebhookURL:                 "https://hooks.slack.com/test",
		WebhookSecret:                   "s3cret",
		MongoURI:                        "",
		ConfigCacheTTLSeconds:           60,
		WebhookProcessingTimeoutSeconds: 300,
		WebhookMaxRetries:               3,
		GitHubAPIMaxRetries:             5,
	}

	container, err := services.NewServiceContainer(config)
	require.NoError(t, err)

	handler := services.ConfigDiagnosticHandler(container, "v1.2.3-test")

	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var resp services.ConfigDiagnosticResponse
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Verify version
	assert.Equal(t, "v1.2.3-test", resp.Version)

	// Verify environment fields
	env := resp.Environment
	assert.Equal(t, "8080", env.Port)
	assert.True(t, env.DryRun)
	assert.True(t, env.UseMainConfig)
	assert.Equal(t, ".copier/main.yaml", env.EffectiveConfig)
	assert.Equal(t, "test-owner", env.ConfigRepoOwner)
	assert.Equal(t, "test-repo", env.ConfigRepoName)
	assert.Equal(t, "main", env.ConfigRepoBranch)
	assert.Equal(t, "/events", env.WebserverPath)
	assert.True(t, env.MetricsEnabled)
	assert.True(t, env.SlackEnabled)
	assert.False(t, env.AuditEnabled)
	assert.Equal(t, 60, env.ConfigCacheTTLSeconds)
	assert.Equal(t, 300, env.WebhookProcessingTimeoutSeconds)
	assert.Equal(t, 3, env.WebhookMaxRetries)
	assert.Equal(t, 5, env.GitHubAPIMaxRetries)

	// Secrets should be redacted
	assert.Equal(t, "[SET]", env.WebhookSecret)
	assert.Equal(t, "[SET]", env.SlackWebhook)
	assert.Equal(t, "[NOT SET]", env.MongoURI)

	// Config loading will fail (no real GitHub client), but the endpoint still works
	assert.NotEmpty(t, resp.LoadError)
	assert.Nil(t, resp.Workflows)
}

func TestConfigDiagnosticHandler_WorkflowSummary(t *testing.T) {
	// Test the workflow summary with a mock config loader that returns a valid config
	config := &configs.Config{
		ConfigRepoOwner: "test-owner",
		ConfigRepoName:  "test-repo",
	}

	container, err := services.NewServiceContainer(config)
	require.NoError(t, err)

	// Replace the config loader with one that returns test workflows
	container.ConfigLoader = &mockConfigLoaderForDiagnostic{
		config: &types.YAMLConfig{
			Workflows: []types.Workflow{
				{
					Name:        "copy-go-examples",
					Source:      types.Source{Repo: "org/source", Branch: "main"},
					Destination: types.Destination{Repo: "org/dest", Branch: "main"},
					Transformations: []types.Transformation{
						{Move: &types.MoveTransform{From: "examples", To: "code"}},
					},
					CommitStrategy: &types.CommitStrategyConfig{Type: "pull_request"},
				},
				{
					Name:        "copy-js-examples",
					Source:      types.Source{Repo: "org/source", Branch: "main"},
					Destination: types.Destination{Repo: "org/dest-2", Branch: "develop"},
					Transformations: []types.Transformation{
						{Glob: &types.GlobTransform{Pattern: "**/*.js", Transform: "js/${relative_path}"}},
						{Glob: &types.GlobTransform{Pattern: "**/*.ts", Transform: "ts/${relative_path}"}},
					},
					Exclude: []string{"node_modules"},
				},
			},
		},
	}

	handler := services.ConfigDiagnosticHandler(container, "v0.0.0-test")

	req := httptest.NewRequest("GET", "/config", nil)
	w := httptest.NewRecorder()

	handler(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp services.ConfigDiagnosticResponse
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Empty(t, resp.LoadError)
	require.Len(t, resp.Workflows, 2)

	wf1 := resp.Workflows[0]
	assert.Equal(t, "copy-go-examples", wf1.Name)
	assert.Equal(t, "org/source", wf1.SourceRepo)
	assert.Equal(t, "main", wf1.SourceBranch)
	assert.Equal(t, "org/dest", wf1.DestRepo)
	assert.Equal(t, "main", wf1.DestBranch)
	assert.Equal(t, "pull_request", wf1.CommitStrategy)
	assert.Equal(t, 1, wf1.Transforms)

	wf2 := resp.Workflows[1]
	assert.Equal(t, "copy-js-examples", wf2.Name)
	assert.Equal(t, "org/dest-2", wf2.DestRepo)
	assert.Equal(t, "develop", wf2.DestBranch)
	assert.Equal(t, "direct", wf2.CommitStrategy) // nil commit_strategy defaults to "direct"
	assert.Equal(t, 2, wf2.Transforms)
	assert.Equal(t, []string{"node_modules"}, wf2.Exclude)
}

// mockConfigLoaderForDiagnostic returns a static config for testing the diagnostic endpoint.
type mockConfigLoaderForDiagnostic struct {
	config *types.YAMLConfig
}

func (m *mockConfigLoaderForDiagnostic) LoadConfig(_ context.Context, _ *configs.Config) (*types.YAMLConfig, error) {
	return m.config, nil
}

func (m *mockConfigLoaderForDiagnostic) LoadConfigFromContent(_ string, _ string) (*types.YAMLConfig, error) {
	return m.config, nil
}

func TestMetricsCollector_ConcurrentAccess(t *testing.T) {
	collector := services.NewMetricsCollector()
	fileStateService := services.NewFileStateService()

	done := make(chan bool)

	// Concurrent writes
	go func() {
		for i := 0; i < 100; i++ {
			collector.RecordWebhookReceived()
			collector.RecordWebhookProcessed(10 * time.Millisecond)
			time.Sleep(1 * time.Millisecond)
		}
		done <- true
	}()

	// Concurrent reads
	go func() {
		for i := 0; i < 100; i++ {
			_ = collector.GetMetrics(fileStateService)
			time.Sleep(1 * time.Millisecond)
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// Should not crash and should have recorded metrics
	metrics := collector.GetMetrics(fileStateService)
	assert.Greater(t, metrics.Webhooks.Received, int64(0))
}
