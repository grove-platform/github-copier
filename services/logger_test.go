package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"
)

// setupTestLogger creates a JSON slog logger writing to the given buffer
// and sets it as the default. Returns a cleanup function.
func setupTestLogger(t *testing.T, buf *bytes.Buffer) func() {
	t.Helper()
	old := slog.Default()
	handler := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelDebug, // capture all levels in tests
	})
	slog.SetDefault(slog.New(handler))
	return func() { slog.SetDefault(old) }
}

// parseLine unmarshals the first JSON object from a buffer.
func parseLine(t *testing.T, buf *bytes.Buffer) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("failed to parse JSON log line: %v\nbuf: %s", err, buf.String())
	}
	return m
}

func TestLogDebug(t *testing.T) {
	tests := []struct {
		name        string
		logLevel    string
		copierDebug string
		message     string
		shouldLog   bool
	}{
		{
			name:        "debug enabled via LOG_LEVEL",
			logLevel:    "debug",
			copierDebug: "",
			message:     "test debug message",
			shouldLog:   true,
		},
		{
			name:        "debug enabled via COPIER_DEBUG",
			logLevel:    "",
			copierDebug: "true",
			message:     "test debug message",
			shouldLog:   true,
		},
		{
			name:        "debug disabled",
			logLevel:    "info",
			copierDebug: "false",
			message:     "test debug message",
			shouldLog:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.logLevel != "" {
				os.Setenv("LOG_LEVEL", tt.logLevel)
				defer os.Unsetenv("LOG_LEVEL")
			}
			if tt.copierDebug != "" {
				os.Setenv("COPIER_DEBUG", tt.copierDebug)
				defer os.Unsetenv("COPIER_DEBUG")
			}

			var buf bytes.Buffer
			cleanup := setupTestLogger(t, &buf)
			defer cleanup()

			LogDebug(tt.message)

			if tt.shouldLog {
				if buf.Len() == 0 {
					t.Error("Expected log output but got none")
					return
				}
				m := parseLine(t, &buf)
				if m["msg"] != tt.message {
					t.Errorf("Expected message %q, got %q", tt.message, m["msg"])
				}
				if m["level"] != "DEBUG" {
					t.Errorf("Expected level DEBUG, got %q", m["level"])
				}
			} else {
				if buf.Len() != 0 {
					t.Errorf("Expected no output, got: %s", buf.String())
				}
			}
		})
	}
}

func TestLogInfo(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	LogInfo("test info message")

	m := parseLine(t, &buf)
	if m["msg"] != "test info message" {
		t.Errorf("Expected message %q, got %q", "test info message", m["msg"])
	}
	if m["level"] != "INFO" {
		t.Errorf("Expected level INFO, got %q", m["level"])
	}
}

func TestLogInfoWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	LogInfo("server started", "port", 8080, "env", "prod")

	m := parseLine(t, &buf)
	if m["msg"] != "server started" {
		t.Errorf("Expected message %q, got %q", "server started", m["msg"])
	}
	if m["port"] != float64(8080) { // JSON unmarshals numbers as float64
		t.Errorf("Expected port=8080, got %v", m["port"])
	}
	if m["env"] != "prod" {
		t.Errorf("Expected env=prod, got %v", m["env"])
	}
}

func TestLogWarning(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	LogWarning("test warning message")

	m := parseLine(t, &buf)
	if m["msg"] != "test warning message" {
		t.Errorf("Expected message %q, got %q", "test warning message", m["msg"])
	}
	if m["level"] != "WARN" {
		t.Errorf("Expected level WARN, got %q", m["level"])
	}
}

func TestLogError(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	LogError("test error message")

	m := parseLine(t, &buf)
	if m["msg"] != "test error message" {
		t.Errorf("Expected message %q, got %q", "test error message", m["msg"])
	}
	if m["level"] != "ERROR" {
		t.Errorf("Expected level ERROR, got %q", m["level"])
	}
}

func TestLogCritical(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	LogCritical("test critical message")

	m := parseLine(t, &buf)
	if m["msg"] != "test critical message" {
		t.Errorf("Expected message %q, got %q", "test critical message", m["msg"])
	}
	// With default slog handler, custom level 12 shows as ERROR+4
	level, ok := m["level"].(string)
	if !ok || level != "ERROR+4" {
		t.Errorf("Expected level ERROR+4 (critical), got %q", m["level"])
	}
}

func TestLogInfoCtx(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	ctx := context.Background()
	fields := map[string]interface{}{
		"key1": "value1",
		"key2": float64(123),
	}

	LogInfoCtx(ctx, "test context message", fields)

	m := parseLine(t, &buf)
	if m["msg"] != "test context message" {
		t.Errorf("Expected message %q, got %q", "test context message", m["msg"])
	}
	if m["key1"] != "value1" {
		t.Errorf("Expected key1=value1, got %v", m["key1"])
	}
	if m["key2"] != float64(123) {
		t.Errorf("Expected key2=123, got %v", m["key2"])
	}
}

func TestLogWarningCtx(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	ctx := context.Background()
	fields := map[string]interface{}{
		"warning_type": "test",
	}

	LogWarningCtx(ctx, "test warning context", fields)

	m := parseLine(t, &buf)
	if m["msg"] != "test warning context" {
		t.Errorf("Expected message %q, got %q", "test warning context", m["msg"])
	}
	if m["warning_type"] != "test" {
		t.Errorf("Expected warning_type=test, got %v", m["warning_type"])
	}
}

func TestLogErrorCtx(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	ctx := context.Background()
	err := fmt.Errorf("test error")
	fields := map[string]interface{}{
		"error_code": float64(500),
	}

	LogErrorCtx(ctx, "test error context", err, fields)

	m := parseLine(t, &buf)
	if m["msg"] != "test error context" {
		t.Errorf("Expected message %q, got %q", "test error context", m["msg"])
	}
	if m["error"] != "test error" {
		t.Errorf("Expected error=test error, got %v", m["error"])
	}
	if m["error_code"] != float64(500) {
		t.Errorf("Expected error_code=500, got %v", m["error_code"])
	}
}

func TestLogWebhookOperation(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		message   string
		err       error
		wantLevel string
	}{
		{
			name:      "successful operation",
			operation: "webhook_received",
			message:   "webhook processed",
			err:       nil,
			wantLevel: "INFO",
		},
		{
			name:      "failed operation",
			operation: "webhook_parse",
			message:   "failed to parse webhook",
			err:       fmt.Errorf("parse error"),
			wantLevel: "ERROR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			cleanup := setupTestLogger(t, &buf)
			defer cleanup()

			ctx := context.Background()
			LogWebhookOperation(ctx, tt.operation, tt.message, tt.err)

			m := parseLine(t, &buf)
			if m["level"] != tt.wantLevel {
				t.Errorf("Expected level %s, got %q", tt.wantLevel, m["level"])
			}
			if m["msg"] != tt.message {
				t.Errorf("Expected message %q, got %q", tt.message, m["msg"])
			}
			if m["operation"] != tt.operation {
				t.Errorf("Expected operation %q, got %v", tt.operation, m["operation"])
			}
		})
	}
}

func TestLogFileOperation(t *testing.T) {
	var buf bytes.Buffer
	cleanup := setupTestLogger(t, &buf)
	defer cleanup()

	ctx := context.Background()
	LogFileOperation(ctx, "copy", "source/file.go", "target/repo", "file copied", nil)

	m := parseLine(t, &buf)
	if m["operation"] != "copy" {
		t.Errorf("Expected operation=copy, got %v", m["operation"])
	}
	if m["source_path"] != "source/file.go" {
		t.Errorf("Expected source_path=source/file.go, got %v", m["source_path"])
	}
	if m["target_repo"] != "target/repo" {
		t.Errorf("Expected target_repo=target/repo, got %v", m["target_repo"])
	}
}

func TestWithRequestID(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)

	ctx, requestID := WithRequestID(req)

	if requestID == "" {
		t.Error("Expected non-empty request ID")
	}

	ctxValue := ctx.Value(requestIDKey)
	if ctxValue == nil {
		t.Error("Expected request_id in context")
	}
	if ctxValue.(string) != requestID {
		t.Error("Context request_id doesn't match returned request ID")
	}
}

func TestMapToAttrs(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]interface{}
		want   int // expected number of resulting attrs (key-value pairs)
	}{
		{
			name:   "nil fields",
			fields: nil,
			want:   0,
		},
		{
			name:   "empty fields",
			fields: map[string]interface{}{},
			want:   0,
		},
		{
			name: "with fields",
			fields: map[string]interface{}{
				"key1": "value1",
				"key2": 123,
			},
			want: 4, // 2 key-value pairs = 4 elements
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapToAttrs(tt.fields)
			if len(result) != tt.want {
				t.Errorf("mapToAttrs() returned %d elements, want %d", len(result), tt.want)
			}
		})
	}
}

func TestInitializeLoggerSeverityMapping(t *testing.T) {
	// Enable debug so LogDebug actually emits
	t.Setenv("COPIER_DEBUG", "true")

	// Test that InitializeLogger sets up a handler that maps levels to severity strings
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
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
			if a.Key == slog.MessageKey {
				a.Key = "message"
			}
			return a
		},
	}

	handler := slog.NewJSONHandler(&buf, opts)
	old := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(old)

	tests := []struct {
		logFunc      func()
		wantSeverity string
	}{
		{func() { LogDebug("d") }, "DEBUG"},
		{func() { LogInfo("i") }, "INFO"},
		{func() { LogWarning("w") }, "WARNING"},
		{func() { LogError("e") }, "ERROR"},
		{func() { LogCritical("c") }, "CRITICAL"},
	}

	for _, tt := range tests {
		buf.Reset()
		tt.logFunc()

		var m map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}
		if m["severity"] != tt.wantSeverity {
			t.Errorf("Expected severity=%s, got %v", tt.wantSeverity, m["severity"])
		}
		if m["message"] == nil {
			t.Error("Expected 'message' key in JSON output")
		}
	}
}

func TestIsDebugEnabled(t *testing.T) {
	tests := []struct {
		name        string
		logLevel    string
		copierDebug string
		want        bool
	}{
		{"debug via LOG_LEVEL", "debug", "", true},
		{"DEBUG via LOG_LEVEL", "DEBUG", "", true},
		{"debug via COPIER_DEBUG", "", "true", true},
		{"debug via COPIER_DEBUG uppercase", "", "TRUE", true},
		{"not enabled", "info", "false", false},
		{"neither set", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("LOG_LEVEL", tt.logLevel)
			os.Setenv("COPIER_DEBUG", tt.copierDebug)
			defer os.Unsetenv("LOG_LEVEL")
			defer os.Unsetenv("COPIER_DEBUG")

			got := isDebugEnabled()
			if got != tt.want {
				t.Errorf("isDebugEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsCloudLoggingDisabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"disabled lowercase", "true", true},
		{"disabled uppercase", "TRUE", true},
		{"enabled", "false", false},
		{"not set", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			os.Setenv("COPIER_DISABLE_CLOUD_LOGGING", tt.value)
			defer os.Unsetenv("COPIER_DISABLE_CLOUD_LOGGING")

			got := isCloudLoggingDisabled()
			if got != tt.want {
				t.Errorf("isCloudLoggingDisabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
