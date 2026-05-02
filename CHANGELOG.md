# Changelog

All notable changes to the github-copier application are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [v0.4.2] - 2026-05-02

### Fixed

- **Version always showing "dev"** — `--set-build-env-vars` does not propagate to Docker `ARG` values in Cloud Run's buildless deploy path. App now reads `APP_VERSION` env var at startup as a runtime fallback; CI deploy sets it via `--set-env-vars`.
- **Disconnected chip diagnosis** — heartbeat `catch` handler now logs the fetch error to `console.error` so the root cause is visible in DevTools when the chip appears.

## [v0.4.1] - 2026-05-02

### Fixed

- **Anthropic API key trailing newline** — secrets loaded from Secret Manager are now trimmed of surrounding whitespace before use, preventing `invalid header field value` errors when the secret was stored with a trailing newline.

## [v0.4.0] - 2026-05-02

### Added

- **Operator UI — comprehensive writer + operator dashboard** at `/operator/` (`OPERATOR_UI_ENABLED=true`). Five tabs (Overview, Webhooks, Audit, Workflows, System), sticky status bar, dark mode, keyboard shortcuts, shareable URLs, and a writer/operator mode toggle persisted to localStorage.
- **GitHub PAT authentication** — users sign in with their personal access token; role is derived from their permission on `OPERATOR_AUTH_REPO` (admin/maintain → operator, write/triage/read → writer). Operator actions (replay, release, AI settings) require an explicit admin or maintain grant, since most writers have `write` on the auth repo. Replay additionally enforces read access on the source repo for that specific delivery.
- **AI rule suggester** — paste a source path and desired target state, receive a suggested workflow rule with self-verification via the in-process `PatternMatcher`. Two providers supported:
  - **Anthropic (hosted)** — default for Cloud Run. API key loaded from Secret Manager via `ANTHROPIC_API_KEY_SECRET_NAME`. No infra required; operators switch between Haiku / Sonnet / Opus from the UI.
  - **Ollama (local)** — for dev or self-hosted deployments. UI manages connection, model pulls, deletes, and active-model switching without a redeploy.
- **Writer-facing features** — workflow browser with per-rule coverage, PR lookup by URL, recent copies feed, file match tester (with clear button and Python-style `(?P<name>)` regex translation for in-browser use), PR timeline, and in-app help overlay.
- **Per-delivery log viewer** — context-tagged ring buffer captures logs per webhook delivery, surfaced in an audit drawer alongside the trace and outcome summary.
- **Audit event enrichment** — `processed_ok` traces now include destination repo(s), files matched / uploaded / failed, and commit SHA.
- **Startup banner** — Operator UI, auth repo, AI model, and AI base URL are now surfaced when the app boots (local and Cloud Run).

### Changed

- **MongoDB audit logging enabled in production** — the Cloud Run deploy previously forced `AUDIT_ENABLED=false`; it is now `true`, aligning with the v0.3.0 "enabled by default" change.
- **Operator auth hardened** — token-based auth (`OPERATOR_UI_TOKEN`) removed entirely; GitHub PAT is the only supported mechanism. `OPERATOR_UI_ENABLED=true` now requires `OPERATOR_AUTH_REPO` at config load (validated in `validateOperatorAuth`).
- **`createPullRequest` skipped for empty commits** — `commitFilesToBranch` now returns an `errTreeUnchanged` sentinel so `addFilesViaPR` no longer calls the GitHub PR API with an unchanged tree (previously 422'd).
- **MongoDB driver v2 ObjectID decoding** — audit reads set `ObjectIDAsHexString: true` to avoid "error decoding key `_id`" on queries.

### Fixed

- **gosec G107 / G704 SSRF findings** — GitHub API URL construction in `services/operator_auth.go` now validates path components against strict RE2-compatible whitelists (`ghUsernameRe`, `ghRepoNameRe`) and escapes them with `url.PathEscape` before request construction; `slack_notifier.go` `#nosec` annotation extended to cover `NewRequestWithContext`.
- **Keyboard-shortcut overlay wouldn't close** — `.help-bg[hidden]` now wins over the base `display:flex`.
- **File match tester returned no matches for Java files** — JavaScript `RegExp` does not support Python-style `(?P<name>)` named groups; the tester now rewrites `(?P<` → `(?<` before compilation.

### Security

- **Token auth removed** — the operator UI no longer accepts a shared bearer token; all access is per-user via GitHub PAT with repo-scoped permission checks.

## [v0.3.1] - 2026-04-30

### Fixed

- **Config cache corruption** — `CachedConfigLoader` no longer returns stale entries when filtering workflows by source repo.

### Security

- **Removed unneeded config files** from the repository.

## [v0.3.0] - 2026-04-14

### Changed

- **Audit logging enabled** — MongoDB audit logging is now enabled by default.

### Fixed

- **Missing `#nosec G706` annotations** — Added suppression comments to `LogInfo` and `LogError` for gosec false positives.

### Security

- **gRPC vulnerability fix** — Upgraded `google.golang.org/grpc` to v1.79.3 to address CVE-2026-33186.

## [v0.2.0] - 2026-02-20

### Added

- **`.golangci.yml` config** — Pinned linter and formatter configuration (v2 format) for consistent CI and local behavior. Enabled linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `misspell`, `revive`.
- **Structured error alerting** — `ErrorEvent` now includes `DeliveryID` and `Attempts` fields. Slack failure notifications include the GitHub delivery ID and attempt count for full traceability.
- **Integration test for target repo batching** — `TestIntegration_TargetRepoBatching_MixedStrategies` verifies that workflows with different commit strategies produce separate operations, while same-strategy workflows batch correctly.
- **End-to-end integration tests** — `TestIntegration_MergedPR_DirectCommit` covers the full webhook-to-commit pipeline; additional tests cover no-matching-workflows, config-load errors, and webhook signature verification. 
- **Config reference doc** — `docs/CONFIG-REFERENCE.md` provides a single-page reference for all environment variables and workflow YAML schema. 
- **Webhook routing guide** — Added a "Webhook Routing" section to `docs/LOCAL-TESTING.md` documenting how to avoid dual-delivery (local + Cloud Run processing the same webhook). 
- **Webhook processing timeout** — Background goroutine now applies `context.WithTimeout` (configurable via `WEBHOOK_PROCESSING_TIMEOUT_SECONDS`, default 300s). 
- **Retry with exponential backoff** — `processWebhookWithRetry` retries failed webhook processing with configurable max retries and initial delay. Panics are recovered and retried. Slack alert sent after exhaustion. 
- **Graceful partial failure** — `processFilesWithWorkflows` processes each workflow independently and returns per-workflow errors. One workflow failure no longer blocks others. 
- **Config caching** — `CachedConfigLoader` caches resolved workflow configs with a configurable TTL (default 5 min, via `CONFIG_CACHE_TTL_SECONDS`). 
- **Parallel file fetching** — `ProcessWorkflow` now fetches file contents concurrently via `errgroup` (concurrency limit of 5). 
- **PR deduplication** — `addFilesViaPR` checks for existing `copier/*` PRs before creating new ones; pushes to existing branch and updates metadata instead.
- **Empty commit prevention** — `createCommitTree` returns base tree SHA; commits are skipped when the new tree is identical to HEAD.
- **Mixed commit strategy fix** — `UploadKey` now includes `CommitStrategy`, separating write operations for `direct` vs `pull_request` workflows targeting the same repo. Config-time warning for conflicting strategies.
- **PR metadata overwrite logging** — Logs when a subsequent workflow overwrites a batched commit message or PR title.
- **Health check probes** — Liveness (`/health`) and readiness (`/ready`) endpoints.
- **Webhook idempotency** — In-memory `DeliveryTracker` prevents duplicate processing of the same `X-GitHub-Delivery` header within a single instance.
- **Rate limiting** — GitHub API retry logic with exponential backoff.
- **CLI tools** — `config-validator`, `test-webhook`, and `test-pem` utilities under `cmd/`.
- **`/config` diagnostic endpoint** — Read-only endpoint showing resolved runtime config (secrets redacted) and workflow summary.
- **Transient vs permanent error classification** — `IsPermanentError()` detects non-retryable failures (404, 403, config validation, etc.); retry loop skips retries immediately for permanent errors.
- **Version stamping** — Binary version set via `-ldflags` at build time; exposed on `/health`, `/config`, startup banner, and `-version` flag.
- **Release script** — `scripts/release.sh` automates CHANGELOG update, git tagging, push, and GitHub Release creation.

### Changed

- **Go version** — Upgraded to Go 1.26.0.
- **golangci-lint** — Upgraded to v2.9.0 (action v7 in CI).
- **go-github** — Upgraded to v82; replaced deprecated `github.String`/`Int`/`Bool` with `github.Ptr`.
- **Logging** — Migrated to `log/slog` with structured JSON output.
- **Pre-commit hooks** — `golangci-lint` hook uses `language: system` with `--fix`; requires local v2.9.0 install.
- **App banner** — Now displays version and `EffectiveConfigFile()` instead of the legacy `ConfigFile` default.
- **CI deploy trigger** — Deployment now triggers on version tag pushes (`v*`) instead of every push to `main`. Tags stamp the version into the Cloud Run revision.
- **Legacy config deprecation** — `DefaultConfigLoader` (single-file config) is marked deprecated with runtime warnings; dead code (`ConfigValidator`, unused struct fields) removed. 

### Fixed

- **CI lint/security failures** — Resolved `golangci-lint` Go version incompatibility, `gosec` taint analysis false positives (G703–G706), and all `errcheck`/`staticcheck`/`unused` issues.
- **gitleaks false positive** — Added `.gitleaksignore` entries for example and test-only PEM keys.
- **Tightened gosec exclusions** — Removed all global `gosec` exclusions from CI; sole remaining false positive suppressed with inline `#nosec G115`. 

### Security
- **Go toolchain directive** — Added `toolchain go1.26.0` to `go.mod` for deterministic builds. 

## [0.1.0] - 2025-12-17

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

## [0.0.1] - Initial Release (Migration from mongodb/code-example-tooling)

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