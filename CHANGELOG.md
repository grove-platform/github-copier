# Changelog

All notable changes to this project will be documented in this file.

## Feb 2026

### Added
- **Rate limit handling** - Custom `RateLimitTransport` (`services/rate_limit.go`) automatically detects 403/429 responses with `X-RateLimit-Remaining` / `Retry-After` headers and retries with backoff
- **Webhook idempotency** - `DeliveryTracker` (`services/delivery_tracker.go`) deduplicates webhook deliveries using `X-GitHub-Delivery` header with TTL-based cleanup
- **Structured logging** - Migrated from `log` to `log/slog` with JSON handler (`services/logger.go`); compatible with Google Cloud Logging severity levels including custom `CRITICAL`
- **Sentinel errors** - Centralized error definitions in `services/errors.go` (`ErrRateLimited`, `ErrNotFound`, etc.)
- **Token manager** - Thread-safe `TokenManager` (`services/token_manager.go`) with `sync.RWMutex` for concurrent token access
- **Readiness endpoint** - `/ready` probe checks GitHub auth, rate limit headroom, and MongoDB connectivity
- **GitHub App manifest** - `github-app-manifest.yml` documenting required permissions and events
- **Integration tests** - End-to-end tests for webhook processing pipeline (`services/integration_test.go`), GitHub auth flow, and extracted helper functions
- **CI/CD improvements** - Added gosec security scanning, Trivy vulnerability scanning, GitHub Environment deployment gates

### Changed
- **Go version** - Upgraded from 1.24.0 to 1.26.0
- **go-github** - Upgraded from v48 to v82 (`github.String` → `github.Ptr`, updated API signatures)
- **mongo-driver** - Upgraded from v1 to v2 (updated import paths, API changes)
- **Error handling** - All errors wrapped with `%w` for `errors.Is()`/`errors.As()` compatibility; removed bare `log.Fatal` calls
- **Function decomposition** - Broke up `handleMergedPRWithContainer` (→ 5 helpers) and `addFilesViaPR` (→ 3 helpers) for readability
- **Dot imports** - Removed all dot imports for explicit package references
- **Health endpoint** - Split into `/health` (liveness) and `/ready` (readiness) with deep dependency checks
- **HTTP method check** - Webhook handler rejects non-POST requests with 405
- **MongoDB timeouts** - Configured explicit connection, server selection, and operation timeouts
- **Dockerfile** - Pinned base image, added non-root user, added `HEALTHCHECK` instruction
- **Environment config** - Moved org-specific values to deployment-time configuration; committed env file contains only non-secret defaults

## 17 Dec 2025

### Added
- CI/CD pipeline with GitHub Actions (`.github/workflows/ci.yml`)
  - Test job
  - Lint job with golangci-lint
  - Security scanning with gosec
  - Build verification
  - Automated deployment to Cloud Run on merge to main (via Workload Identity Federation)
- Pre-commit hooks for secrets detection and Go linting (`.pre-commit-config.yaml`)
- AGENT.md for AI agent context
- Comprehensive test suite for `workflow_processor.go` (843 lines, 94%+ coverage)
- Integration test harness for local testing (`scripts/integration-test.sh`)
- Test environment configuration (`testdata/.env.test`)

### Changed
- Renamed module from `github.com/mongodb/code-example-tooling/code-copier` to `github.com/grove-platform/github-copier`
- Renamed binary from `examples-copier` to `github-copier`
- Renamed `test-payloads/` to `testdata/` (Go convention)
- All `log.Fatal` calls replaced with proper error returns for graceful error handling
- `FileStateService.filesToDeprecate` changed from single-entry map to slice-based accumulation

### Fixed
- Deprecation file accumulation bug: multiple deprecated files now correctly accumulate instead of overwriting
- Nil pointer dereference bugs across GitHub API calls in:
  - `services/github_read.go`
  - `services/github_write_to_source.go`
  - `services/main_config_loader.go`
  - `services/config_loader.go`
- DELETED file status handling: GitHub GraphQL API returns uppercase `DELETED` but code checked for lowercase `removed`
- Graceful shutdown now properly waits for in-flight requests and cleans up resources

### Security
- Added gitleaks pre-commit hook for secrets detection
- Added gosec security scanning in CI pipeline

## Initial Release (Migration from mongodb/code-example-tooling)

### Features
- Webhook service for automated file copying on PR merge
- Pattern matching support: prefix, glob, regex
- Transformation types: move, copy, glob, regex
- Main config system with `$ref` support for distributed workflow configs
- Commit strategies: direct commit or pull request
- Health and metrics endpoints
- Slack notifications for operational visibility
- MongoDB audit logging (optional)
- Google Cloud Logging integration
- Dry-run mode for testing

