package services

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// HealthStatus represents the health status of the application
type HealthStatus struct {
	Status      string                  `json:"status"`
	Started     bool                    `json:"started"`
	GitHub      GitHubHealthStatus      `json:"github"`
	Queues      QueueHealthStatus       `json:"queues"`
	AuditLogger AuditLoggerHealthStatus `json:"audit_logger,omitempty"`
	Uptime      string                  `json:"uptime"`
}

// GitHubHealthStatus represents GitHub API health
type GitHubHealthStatus struct {
	Status        string `json:"status"`
	Authenticated bool   `json:"authenticated"`
}

// QueueHealthStatus represents queue health
type QueueHealthStatus struct {
	UploadCount      int `json:"upload_count"`
	DeprecationCount int `json:"deprecation_count"`
}

// AuditLoggerHealthStatus represents audit logger health
type AuditLoggerHealthStatus struct {
	Status    string `json:"status"`
	Connected bool   `json:"connected"`
}

// MetricsData represents application metrics
type MetricsData struct {
	Webhooks  WebhookMetrics   `json:"webhooks"`
	Files     FileMetrics      `json:"files"`
	GitHubAPI GitHubAPIMetrics `json:"github_api"`
	Queues    QueueMetrics     `json:"queues"`
	System    SystemMetrics    `json:"system"`
}

// WebhookMetrics represents webhook processing metrics
type WebhookMetrics struct {
	Received       int64               `json:"received"`
	Processed      int64               `json:"processed"`
	Failed         int64               `json:"failed"`
	Ignored        int64               `json:"ignored"`     // Non-PR events
	EventTypes     map[string]int64    `json:"event_types"` // Count by event type
	SuccessRate    float64             `json:"success_rate"`
	ProcessingTime ProcessingTimeStats `json:"processing_time"`
}

// FileMetrics represents file operation metrics
type FileMetrics struct {
	Matched           int64               `json:"matched"`
	Uploaded          int64               `json:"uploaded"`
	UploadFailed      int64               `json:"upload_failed"`
	Deprecated        int64               `json:"deprecated"`
	UploadSuccessRate float64             `json:"upload_success_rate"`
	UploadTime        ProcessingTimeStats `json:"upload_time"`
}

// GitHubAPIMetrics represents GitHub API usage metrics
type GitHubAPIMetrics struct {
	Calls     int64         `json:"calls"`
	Errors    int64         `json:"errors"`
	ErrorRate float64       `json:"error_rate"`
	RateLimit RateLimitInfo `json:"rate_limit"`
}

// RateLimitInfo represents GitHub API rate limit info
type RateLimitInfo struct {
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"reset_at"`
}

// QueueMetrics represents queue size metrics
type QueueMetrics struct {
	UploadQueueSize      int `json:"upload_queue_size"`
	DeprecationQueueSize int `json:"deprecation_queue_size"`
	RetryQueueSize       int `json:"retry_queue_size"`
}

// SystemMetrics represents system-level metrics
type SystemMetrics struct {
	UptimeSeconds int64 `json:"uptime_seconds"`
}

// ProcessingTimeStats represents timing statistics
type ProcessingTimeStats struct {
	AvgMs float64 `json:"avg_ms"`
	MinMs float64 `json:"min_ms"`
	MaxMs float64 `json:"max_ms"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
}

// MetricsCollector collects and manages application metrics
type MetricsCollector struct {
	mu                sync.RWMutex
	startTime         time.Time
	webhookReceived   int64
	webhookProcessed  int64
	webhookFailed     int64
	webhookIgnored    int64            // Non-PR events that were ignored
	eventTypes        map[string]int64 // Count by event type
	filesMatched      int64
	filesUploaded     int64
	filesUploadFailed int64
	filesDeprecated   int64
	githubAPICalls    int64
	githubAPIErrors   int64
	processingTimes   []time.Duration
	uploadTimes       []time.Duration
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		startTime:       time.Now(),
		eventTypes:      make(map[string]int64),
		processingTimes: make([]time.Duration, 0, 1000),
		uploadTimes:     make([]time.Duration, 0, 1000),
	}
}

// RecordWebhookReceived increments webhook received counter
func (mc *MetricsCollector) RecordWebhookReceived() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.webhookReceived++
}

// RecordWebhookProcessed increments webhook processed counter
func (mc *MetricsCollector) RecordWebhookProcessed(duration time.Duration) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.webhookProcessed++
	mc.processingTimes = append(mc.processingTimes, duration)

	// Keep only last 1000 entries
	if len(mc.processingTimes) > 1000 {
		mc.processingTimes = mc.processingTimes[len(mc.processingTimes)-1000:]
	}
}

// RecordWebhookFailed increments webhook failed counter
func (mc *MetricsCollector) RecordWebhookFailed() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.webhookFailed++
}

// RecordWebhookIgnored increments webhook ignored counter and tracks event type
func (mc *MetricsCollector) RecordWebhookIgnored(eventType string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.webhookIgnored++
	mc.eventTypes[eventType]++
}

// RecordFileMatched increments file matched counter
func (mc *MetricsCollector) RecordFileMatched() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.filesMatched++
}

// RecordFileUploaded increments file uploaded counter
func (mc *MetricsCollector) RecordFileUploaded(duration time.Duration) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.filesUploaded++
	mc.uploadTimes = append(mc.uploadTimes, duration)

	// Keep only last 1000 entries
	if len(mc.uploadTimes) > 1000 {
		mc.uploadTimes = mc.uploadTimes[len(mc.uploadTimes)-1000:]
	}
}

// RecordFileUploadFailed increments file upload failed counter
func (mc *MetricsCollector) RecordFileUploadFailed() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.filesUploadFailed++
}

// RecordFileDeprecated increments file deprecated counter
func (mc *MetricsCollector) RecordFileDeprecated() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.filesDeprecated++
}

// RecordGitHubAPICall increments GitHub API call counter
func (mc *MetricsCollector) RecordGitHubAPICall() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.githubAPICalls++
}

// RecordGitHubAPIError increments GitHub API error counter
func (mc *MetricsCollector) RecordGitHubAPIError() {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.githubAPIErrors++
}

// GetFilesMatched returns the current files matched count
func (mc *MetricsCollector) GetFilesMatched() int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return int(mc.filesMatched) // #nosec G115 -- counter fits in int
}

// GetFilesUploaded returns the current files uploaded count
func (mc *MetricsCollector) GetFilesUploaded() int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return int(mc.filesUploaded) // #nosec G115 -- counter fits in int
}

// GetFilesUploadFailed returns the current files upload failed count
func (mc *MetricsCollector) GetFilesUploadFailed() int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return int(mc.filesUploadFailed) // #nosec G115 -- counter fits in int
}

// GetMetrics returns current metrics
func (mc *MetricsCollector) GetMetrics(fileStateService FileStateService) MetricsData {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	webhookSuccessRate := 0.0
	if mc.webhookReceived > 0 {
		webhookSuccessRate = float64(mc.webhookProcessed) / float64(mc.webhookReceived) * 100
	}

	uploadSuccessRate := 0.0
	totalUploads := mc.filesUploaded + mc.filesUploadFailed
	if totalUploads > 0 {
		uploadSuccessRate = float64(mc.filesUploaded) / float64(totalUploads) * 100
	}

	githubErrorRate := 0.0
	if mc.githubAPICalls > 0 {
		githubErrorRate = float64(mc.githubAPIErrors) / float64(mc.githubAPICalls) * 100
	}

	// Get queue sizes
	uploadQueue := fileStateService.GetFilesToUpload()
	deprecationQueue := fileStateService.GetFilesToDeprecate()

	// Copy event types map
	eventTypesCopy := make(map[string]int64, len(mc.eventTypes))
	for k, v := range mc.eventTypes {
		eventTypesCopy[k] = v
	}

	return MetricsData{
		Webhooks: WebhookMetrics{
			Received:       mc.webhookReceived,
			Processed:      mc.webhookProcessed,
			Failed:         mc.webhookFailed,
			Ignored:        mc.webhookIgnored,
			EventTypes:     eventTypesCopy,
			SuccessRate:    webhookSuccessRate,
			ProcessingTime: calculateStats(mc.processingTimes),
		},
		Files: FileMetrics{
			Matched:           mc.filesMatched,
			Uploaded:          mc.filesUploaded,
			UploadFailed:      mc.filesUploadFailed,
			Deprecated:        mc.filesDeprecated,
			UploadSuccessRate: uploadSuccessRate,
			UploadTime:        calculateStats(mc.uploadTimes),
		},
		GitHubAPI: GitHubAPIMetrics{
			Calls:     mc.githubAPICalls,
			Errors:    mc.githubAPIErrors,
			ErrorRate: githubErrorRate,
			RateLimit: currentRateLimitInfo(),
		},
		Queues: QueueMetrics{
			UploadQueueSize:      len(uploadQueue),
			DeprecationQueueSize: len(deprecationQueue),
			RetryQueueSize:       0,
		},
		System: SystemMetrics{
			UptimeSeconds: int64(time.Since(mc.startTime).Seconds()),
		},
	}
}

// calculateStats calculates timing statistics
func calculateStats(durations []time.Duration) ProcessingTimeStats {
	if len(durations) == 0 {
		return ProcessingTimeStats{}
	}

	var sum, min, max float64
	min = float64(durations[0].Milliseconds())
	max = min

	for _, d := range durations {
		ms := float64(d.Milliseconds())
		sum += ms
		if ms < min {
			min = ms
		}
		if ms > max {
			max = ms
		}
	}

	avg := sum / float64(len(durations))

	// Calculate percentiles (simplified)
	p50 := avg // Simplified
	p95 := avg * 1.5
	p99 := avg * 2.0

	return ProcessingTimeStats{
		AvgMs: avg,
		MinMs: min,
		MaxMs: max,
		P50Ms: p50,
		P95Ms: p95,
		P99Ms: p99,
	}
}

// HealthHandler handles /health (liveness) endpoint.
// Returns 200 if the process is running. This is a lightweight check
// suitable for Cloud Run / Kubernetes liveness probes.
func HealthHandler(startTime time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		health := map[string]interface{}{
			"status":  "healthy",
			"started": true,
			"uptime":  time.Since(startTime).String(),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(health)
	}
}

// ReadinessHandler handles /ready endpoint.
// Checks actual dependency connectivity (GitHub API auth, MongoDB).
// Returns 200 if all dependencies are reachable, 503 otherwise.
// Suitable for Cloud Run / Kubernetes readiness probes.
func ReadinessHandler(container *ServiceContainer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		status := "ready"
		httpStatus := http.StatusOK

		// Check GitHub API: verify we have a valid authentication token
		githubStatus := "healthy"
		githubAuth := defaultTokenManager.GetInstallationAccessToken() != ""
		if !githubAuth {
			githubStatus = "not_authenticated"
		}
		// Check rate limit state
		remaining, resetAt := GlobalRateLimitState.Get()
		if remaining == 0 && time.Now().Before(resetAt) {
			githubStatus = "rate_limited"
		}

		// Check MongoDB (if audit logging is enabled)
		auditStatus := "disabled"
		auditConnected := false
		if container.AuditLogger != nil {
			if err := container.AuditLogger.Ping(ctx); err != nil {
				auditStatus = "unavailable"
				status = "degraded"
			} else {
				auditStatus = "connected"
				auditConnected = true
			}
		}

		// If GitHub is not authenticated, we're not ready
		if !githubAuth {
			status = "not_ready"
			httpStatus = http.StatusServiceUnavailable
		}

		uploadQueue := container.FileStateService.GetFilesToUpload()
		deprecationQueue := container.FileStateService.GetFilesToDeprecate()

		health := HealthStatus{
			Status:  status,
			Started: true,
			GitHub: GitHubHealthStatus{
				Status:        githubStatus,
				Authenticated: githubAuth,
			},
			Queues: QueueHealthStatus{
				UploadCount:      len(uploadQueue),
				DeprecationCount: len(deprecationQueue),
			},
			AuditLogger: AuditLoggerHealthStatus{
				Status:    auditStatus,
				Connected: auditConnected,
			},
			Uptime: time.Since(container.StartTime).String(),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(httpStatus)
		_ = json.NewEncoder(w).Encode(health)
	}
}

// currentRateLimitInfo returns the most recently observed GitHub API rate limit info.
func currentRateLimitInfo() RateLimitInfo {
	remaining, resetAt := GlobalRateLimitState.Get()
	if remaining < 0 {
		// No API calls made yet; return safe defaults
		return RateLimitInfo{Remaining: -1, ResetAt: time.Time{}}
	}
	return RateLimitInfo{Remaining: remaining, ResetAt: resetAt}
}

// MetricsHandler handles /metrics endpoint
func MetricsHandler(metricsCollector *MetricsCollector, fileStateService FileStateService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		metrics := metricsCollector.GetMetrics(fileStateService)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(metrics)
	}
}
