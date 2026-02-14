package services

import "errors"

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
