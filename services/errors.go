package services

import (
	"errors"
	"net/http"

	"github.com/google/go-github/v82/github"
)

// Sentinel errors for common failure modes. Wrap these with fmt.Errorf("%w", ...)
// so callers can use errors.Is() to detect specific failure categories.
var (
	// ErrAuthentication indicates a GitHub App authentication failure
	// (invalid PEM, expired key, bad JWT, etc.).
	ErrAuthentication = errors.New("github app authentication failed")

	// ErrSecretAccess indicates a failure to retrieve a secret from
	// GCP Secret Manager or from the local environment fallback.
	ErrSecretAccess = errors.New("secret access failed")

	// ErrConfigLoad indicates a failure to load or parse the YAML configuration file.
	ErrConfigLoad = errors.New("config load failed")

	// ErrConfigValidation indicates the configuration was loaded but failed validation.
	ErrConfigValidation = errors.New("config validation failed")

	// ErrContentNil indicates that the GitHub API returned a nil content body
	// for a file that was expected to exist.
	ErrContentNil = errors.New("file content is nil")

	// ErrInstallationNotFound indicates that no GitHub App installation was found
	// for the given organization.
	ErrInstallationNotFound = errors.New("no installation found for organization")

	// ErrMergeConflict indicates a PR or ref update could not be completed
	// due to merge conflicts or non-fast-forward push.
	ErrMergeConflict = errors.New("merge conflict")
)

// permanentSentinels lists sentinel errors that indicate a permanent failure.
// These errors will not resolve by retrying the same operation.
var permanentSentinels = []error{
	ErrAuthentication,
	ErrConfigLoad,
	ErrConfigValidation,
	ErrInstallationNotFound,
	ErrMergeConflict,
}

// permanentHTTPStatuses lists HTTP status codes from the GitHub API that
// indicate a permanent (non-retryable) failure.
var permanentHTTPStatuses = map[int]bool{
	http.StatusNotFound:            true, // 404 — repo, ref, or resource does not exist
	http.StatusForbidden:           true, // 403 — no permission (rate limits handled separately)
	http.StatusUnprocessableEntity: true, // 422 — validation error, merge conflict
	http.StatusConflict:            true, // 409 — ref update conflict
	http.StatusGone:                true, // 410 — resource permanently removed
}

// IsPermanentError returns true if err represents a failure that will not
// resolve by retrying the same operation. The retry loop should break
// immediately when this returns true.
//
// Permanent errors include:
//   - Known sentinel errors (config validation, installation not found, merge conflict)
//   - GitHub API responses with 403, 404, 409, 410, or 422 status codes
//
// Note: HTTP 403 from rate limiting is handled separately by rateLimitTransport
// before it reaches this layer. A 403 that reaches here is a true permission denial.
func IsPermanentError(err error) bool {
	if err == nil {
		return false
	}

	// Check sentinel errors
	for _, sentinel := range permanentSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	// Check GitHub API error responses
	var ghErr *github.ErrorResponse
	if errors.As(err, &ghErr) {
		if ghErr.Response != nil {
			return permanentHTTPStatuses[ghErr.Response.StatusCode]
		}
	}

	return false
}
