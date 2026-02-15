# Changelog

All notable changes to the github-copier application are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- **`.golangci.yml` config** — Pinned linter and formatter configuration (v2 format) for consistent CI and local behavior. Enabled linters: `errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `misspell`, `revive`. (#16)
- **Structured error alerting** — `ErrorEvent` now includes `DeliveryID` and `Attempts` fields. Slack failure notifications include the GitHub delivery ID and attempt count for full traceability. (#23)
- **Integration test for target repo batching** — `TestIntegration_TargetRepoBatching_MixedStrategies` verifies that workflows with different commit strategies produce separate operations, while same-strategy workflows batch correctly. (#19)
- **End-to-end integration tests** — `TestIntegration_MergedPR_DirectCommit` covers the full webhook-to-commit pipeline; additional tests cover no-matching-workflows, config-load errors, and webhook signature verification. (#20)
- **Config reference doc** — `docs/CONFIG-REFERENCE.md` provides a single-page reference for all environment variables and workflow YAML schema. (#28)
- **Webhook routing guide** — Added a "Webhook Routing" section to `docs/LOCAL-TESTING.md` documenting how to avoid dual-delivery (local + Cloud Run processing the same webhook). (#29)
- **Webhook processing timeout** — Background goroutine now applies `context.WithTimeout` (configurable via `WEBHOOK_PROCESSING_TIMEOUT_SECONDS`, default 300s). (#9)
- **Retry with exponential backoff** — `processWebhookWithRetry` retries failed webhook processing with configurable max retries and initial delay. Panics are recovered and retried. Slack alert sent after exhaustion. (#7)
- **Graceful partial failure** — `processFilesWithWorkflows` processes each workflow independently and returns per-workflow errors. One workflow failure no longer blocks others. (#6)
- **Config caching** — `CachedConfigLoader` caches resolved workflow configs with a configurable TTL (default 5 min, via `CONFIG_CACHE_TTL_SECONDS`). (#13)
- **Parallel file fetching** — `ProcessWorkflow` now fetches file contents concurrently via `errgroup` (concurrency limit of 5). (#14)
- **PR deduplication** — `addFilesViaPR` checks for existing `copier/*` PRs before creating new ones; pushes to existing branch and updates metadata instead. (#5)
- **Empty commit prevention** — `createCommitTree` returns base tree SHA; commits are skipped when the new tree is identical to HEAD. (#2)
- **Mixed commit strategy fix** — `UploadKey` now includes `CommitStrategy`, separating write operations for `direct` vs `pull_request` workflows targeting the same repo. Config-time warning for conflicting strategies. (#1)
- **PR metadata overwrite logging** — Logs when a subsequent workflow overwrites a batched commit message or PR title. (#3)
- **Health check probes** — Liveness (`/health`) and readiness (`/ready`) endpoints.
- **Webhook idempotency** — In-memory `DeliveryTracker` prevents duplicate processing of the same `X-GitHub-Delivery` header within a single instance.
- **Rate limiting** — GitHub API retry logic with exponential backoff.
- **CLI tools** — `config-validator`, `test-webhook`, and `test-pem` utilities under `cmd/`.

### Changed

- **Go version** — Upgraded to Go 1.26.0.
- **golangci-lint** — Upgraded to v2.9.0 (action v7 in CI).
- **go-github** — Upgraded to v82; replaced deprecated `github.String`/`Int`/`Bool` with `github.Ptr`.
- **Logging** — Migrated to `log/slog` with structured JSON output.
- **Pre-commit hooks** — `golangci-lint` hook uses `language: system` with `--fix`; requires local v2.9.0 install.
- **App banner** — Now displays `EffectiveConfigFile()` instead of the legacy `ConfigFile` default.

### Fixed

- **CI lint/security failures** — Resolved `golangci-lint` Go version incompatibility, `gosec` taint analysis false positives (G703–G706), and all `errcheck`/`staticcheck`/`unused` issues.
- **gitleaks false positive** — Added `.gitleaksignore` entries for example and test-only PEM keys.

### Security

- **Rotated test PEM key** — `.env.test` now contains a purpose-generated test-only RSA key never associated with any real GitHub App. (#10)
