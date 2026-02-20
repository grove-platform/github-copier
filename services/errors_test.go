package services_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/services"
	"github.com/stretchr/testify/assert"
)

func TestIsPermanentError_NilError(t *testing.T) {
	assert.False(t, services.IsPermanentError(nil))
}

func TestIsPermanentError_TransientErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"generic error", fmt.Errorf("something went wrong")},
		{"network timeout", fmt.Errorf("dial tcp: i/o timeout")},
		{"wrapped generic", fmt.Errorf("operation failed: %w", fmt.Errorf("transient"))},
		{"github 500", &github.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusInternalServerError},
			Message:  "Internal Server Error",
		}},
		{"github 502", &github.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusBadGateway},
			Message:  "Bad Gateway",
		}},
		{"github 503", &github.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
			Message:  "Service Unavailable",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, services.IsPermanentError(tt.err), "expected transient, got permanent")
		})
	}
}

func TestIsPermanentError_PermanentSentinels(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		sentinel error
	}{
		{"authentication", services.ErrAuthentication, services.ErrAuthentication},
		{"config load", services.ErrConfigLoad, services.ErrConfigLoad},
		{"config validation", services.ErrConfigValidation, services.ErrConfigValidation},
		{"installation not found", services.ErrInstallationNotFound, services.ErrInstallationNotFound},
		{"merge conflict", services.ErrMergeConflict, services.ErrMergeConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Direct sentinel
			assert.True(t, services.IsPermanentError(tt.err))

			// Wrapped sentinel
			wrapped := fmt.Errorf("webhook failed: %w", tt.err)
			assert.True(t, services.IsPermanentError(wrapped))

			// Double-wrapped sentinel
			doubleWrapped := fmt.Errorf("outer: %w", wrapped)
			assert.True(t, services.IsPermanentError(doubleWrapped))
		})
	}
}

func TestIsPermanentError_NonPermanentSentinels(t *testing.T) {
	// These sentinel errors are NOT classified as permanent — they may
	// resolve on retry (e.g. secrets re-fetched, content retried).
	tests := []struct {
		name string
		err  error
	}{
		{"secret access", services.ErrSecretAccess},
		{"content nil", services.ErrContentNil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, services.IsPermanentError(tt.err))
		})
	}
}

func TestIsPermanentError_GitHubAPIStatuses(t *testing.T) {
	permanentCodes := []struct {
		code int
		name string
	}{
		{http.StatusNotFound, "404 Not Found"},
		{http.StatusForbidden, "403 Forbidden"},
		{http.StatusUnprocessableEntity, "422 Unprocessable Entity"},
		{http.StatusConflict, "409 Conflict"},
		{http.StatusGone, "410 Gone"},
	}

	for _, tt := range permanentCodes {
		t.Run(tt.name, func(t *testing.T) {
			err := &github.ErrorResponse{
				Response: &http.Response{StatusCode: tt.code},
				Message:  tt.name,
			}
			assert.True(t, services.IsPermanentError(err), "expected permanent for %s", tt.name)

			// Also works when wrapped
			wrapped := fmt.Errorf("github: %w", err)
			assert.True(t, services.IsPermanentError(wrapped), "expected permanent for wrapped %s", tt.name)
		})
	}
}

func TestIsPermanentError_GitHubAPINilResponse(t *testing.T) {
	// ErrorResponse with nil Response should not panic and should be treated as transient
	err := &github.ErrorResponse{
		Response: nil,
		Message:  "unknown error",
	}
	assert.False(t, services.IsPermanentError(err))
}
