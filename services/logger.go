package services

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/logging"
	"github.com/grove-platform/github-copier/configs"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

// requestIDKey is the context key for request IDs
const requestIDKey contextKey = "request_id"

// LevelCritical is a custom slog level above Error for critical/fatal issues.
// slog defines Debug=-4, Info=0, Warn=4, Error=8; we use 12 for Critical.
const LevelCritical = slog.Level(12)

// keep a reference to allow flushing/closing
var googleLoggingClient *logging.Client
var gcpLoggingEnabled bool

// googleLoggers maps slog levels to GCP Cloud Logging standard loggers.
// Only populated when GCP Cloud Logging is enabled.
var googleLoggers map[slog.Level]*logging.Logger

// InitializeLogger sets up the slog-based logger with JSON output and optional
// GCP Cloud Logging integration. Call this once at startup.
func InitializeLogger(config *configs.Config) {
	level := slog.LevelInfo
	if isDebugEnabled() {
		level = slog.LevelDebug
	}

	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Rename "level" to "severity" for Cloud Logging JSON compatibility.
			// Cloud Run/GKE auto-parses severity from structured JSON on stdout.
			if a.Key == slog.LevelKey {
				a.Key = "severity"
				lvl := a.Value.Any().(slog.Level)
				switch {
				case lvl >= LevelCritical:
					a.Value = slog.StringValue("CRITICAL")
				case lvl >= slog.LevelError:
					a.Value = slog.StringValue("ERROR")
				case lvl >= slog.LevelWarn:
					a.Value = slog.StringValue("WARNING")
				case lvl >= slog.LevelInfo:
					a.Value = slog.StringValue("INFO")
				default:
					a.Value = slog.StringValue("DEBUG")
				}
			}
			// Rename "msg" to "message" for Cloud Logging compatibility
			if a.Key == slog.MessageKey {
				a.Key = "message"
			}
			return a
		},
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)
	slog.SetDefault(slog.New(handler))

	// Optionally initialize GCP Cloud Logging API client for direct log ingestion
	initGCPLogging(config)
}

// initGCPLogging sets up the GCP Cloud Logging API client if configured.
// This is a secondary logging path — the primary path is JSON to stdout which
// Cloud Run/GKE auto-ingests. The API client can be useful for non-Cloud Run
// deployments or when you need log entries with richer metadata.
func initGCPLogging(config *configs.Config) {
	if isCloudLoggingDisabled() {
		gcpLoggingEnabled = false
		return
	}
	if googleLoggingClient != nil {
		gcpLoggingEnabled = true
		return
	}

	projectId := config.GoogleCloudProjectId
	if projectId == "" {
		slog.Warn("GOOGLE_CLOUD_PROJECT_ID not set, disabling GCP Cloud Logging API client")
		gcpLoggingEnabled = false
		return
	}

	client, err := logging.NewClient(context.Background(), projectId)
	if err != nil {
		slog.Warn("failed to create GCP Cloud Logging client, falling back to stdout only",
			"error", err)
		gcpLoggingEnabled = false
		return
	}
	googleLoggingClient = client
	gcpLoggingEnabled = true

	logName := config.CopierLogName
	if logName == "" {
		logName = "code-copier-log"
	}

	googleLoggers = map[slog.Level]*logging.Logger{
		slog.LevelInfo:  client.Logger(logName),
		slog.LevelWarn:  client.Logger(logName),
		slog.LevelError: client.Logger(logName),
		LevelCritical:   client.Logger(logName),
	}
}

// CloseGoogleLogger flushes and closes the underlying Google logging client, if any.
func CloseGoogleLogger() {
	if googleLoggingClient != nil {
		_ = googleLoggingClient.Close()
	}
}

// gcpSeverity maps slog levels to GCP logging severity.
func gcpSeverity(level slog.Level) logging.Severity {
	switch {
	case level >= LevelCritical:
		return logging.Critical
	case level >= slog.LevelError:
		return logging.Error
	case level >= slog.LevelWarn:
		return logging.Warning
	default:
		return logging.Info
	}
}

// logToGCP sends a log entry to GCP Cloud Logging API if enabled.
func logToGCP(level slog.Level, msg string, attrs ...any) {
	if !gcpLoggingEnabled || googleLoggers == nil {
		return
	}
	logger := googleLoggers[slog.LevelInfo] // default
	if l, ok := googleLoggers[level]; ok {
		logger = l
	}
	if logger == nil {
		return
	}

	// Build payload as a map for structured GCP log entries
	payload := map[string]any{"message": msg}
	for i := 0; i+1 < len(attrs); i += 2 {
		if key, ok := attrs[i].(string); ok {
			payload[key] = attrs[i+1]
		}
	}

	logger.Log(logging.Entry{
		Severity: gcpSeverity(level),
		Payload:  payload,
	})
}

// ──────────────────────────────────────────────
// Convenience logging functions
// ──────────────────────────────────────────────

// LogDebug writes a debug-level log. Only emits when LOG_LEVEL=debug or COPIER_DEBUG=true.
func LogDebug(message string, args ...any) {
	if !isDebugEnabled() {
		return
	}
	slog.Debug(message, args...)
	logToGCP(slog.LevelDebug, message, args...)
}

// LogInfo writes an info-level log.
func LogInfo(message string, args ...any) {
	slog.Info(message, args...) // #nosec G706 -- structured logging; args are key-value pairs, not user input
	logToGCP(slog.LevelInfo, message, args...)
}

// LogWarning writes a warning-level log.
func LogWarning(message string, args ...any) {
	slog.Warn(message, args...) // #nosec G706 -- structured logging; args are key-value pairs, not user input
	logToGCP(slog.LevelWarn, message, args...)
}

// LogError writes an error-level log.
func LogError(message string, args ...any) {
	slog.Error(message, args...) // #nosec G706 -- structured logging; args are key-value pairs, not user input
	logToGCP(slog.LevelError, message, args...)
}

// LogCritical writes a critical-level log (above Error).
func LogCritical(message string, args ...any) {
	slog.Log(context.Background(), LevelCritical, message, args...)
	logToGCP(LevelCritical, message, args...)
}

// LogInfoCtx writes an info-level log with context.
func LogInfoCtx(ctx context.Context, message string, fields map[string]interface{}) {
	slog.InfoContext(ctx, message, mapToAttrs(fields)...)
	logToGCP(slog.LevelInfo, message, mapToAttrs(fields)...)
}

// LogWarningCtx writes a warning-level log with context.
func LogWarningCtx(ctx context.Context, message string, fields map[string]interface{}) {
	slog.WarnContext(ctx, message, mapToAttrs(fields)...)
	logToGCP(slog.LevelWarn, message, mapToAttrs(fields)...)
}

// LogErrorCtx writes an error-level log with context and an optional error.
func LogErrorCtx(ctx context.Context, message string, err error, fields map[string]interface{}) {
	attrs := mapToAttrs(fields)
	if err != nil {
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	slog.ErrorContext(ctx, message, attrs...)
	logToGCP(slog.LevelError, message, attrs...)
}

// LogWebhookOperation logs webhook-related operations.
func LogWebhookOperation(ctx context.Context, operation string, message string, err error, fields ...map[string]interface{}) {
	allFields := make(map[string]interface{})
	allFields["operation"] = operation
	if len(fields) > 0 && fields[0] != nil {
		for k, v := range fields[0] {
			allFields[k] = v
		}
	}
	if err != nil {
		LogErrorCtx(ctx, message, err, allFields)
	} else {
		LogInfoCtx(ctx, message, allFields)
	}
}

// LogFileOperation logs file-related operations.
func LogFileOperation(ctx context.Context, operation string, sourcePath string, targetRepo string, message string, err error, fields ...map[string]interface{}) {
	allFields := make(map[string]interface{})
	allFields["operation"] = operation
	allFields["source_path"] = sourcePath
	if targetRepo != "" {
		allFields["target_repo"] = targetRepo
	}
	if len(fields) > 0 && fields[0] != nil {
		for k, v := range fields[0] {
			allFields[k] = v
		}
	}
	if err != nil {
		LogErrorCtx(ctx, message, err, allFields)
	} else {
		LogInfoCtx(ctx, message, allFields)
	}
}

// LogAndReturnError logs an error and returns (convenience for early-return error paths).
func LogAndReturnError(ctx context.Context, operation string, message string, err error) {
	LogErrorCtx(ctx, message, err, map[string]interface{}{
		"operation": operation,
	})
}

// ──────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────

// mapToAttrs converts a map[string]interface{} to slog key-value pairs.
func mapToAttrs(fields map[string]interface{}) []any {
	if len(fields) == 0 {
		return nil
	}
	attrs := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		attrs = append(attrs, k, v)
	}
	return attrs
}

// WithRequestID adds a request ID to the context and returns both the context and the ID.
func WithRequestID(r *http.Request) (context.Context, string) {
	requestID := fmt.Sprintf("%d", time.Now().UnixNano())
	ctx := context.WithValue(r.Context(), requestIDKey, requestID)
	return ctx, requestID
}

func isDebugEnabled() bool {
	if strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		return true
	}
	return strings.EqualFold(os.Getenv("COPIER_DEBUG"), "true")
}

func isCloudLoggingDisabled() bool {
	return strings.EqualFold(os.Getenv("COPIER_DISABLE_CLOUD_LOGGING"), "true")
}
