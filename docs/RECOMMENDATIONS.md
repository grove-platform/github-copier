# Recommendations & Backlog

Improvement recommendations for the github-copier application, organized by category and priority.

Last updated: 2026-02-15

## Progress Summary

| Category      | Resolved | Remaining |
|---------------|----------|-----------|
| Bugs          | 3        | 0         |
| Reliability   | 5        | 2         |
| Security      | 3        | 0         |
| Performance   | 3        | 0         |
| Code Quality  | 2        | 1         |
| Testing       | 3        | 1         |
| Operational   | 2        | 2         |
| Documentation | 3        | 0         |
| **Total**     | **24**   | **5**     |

## Bugs / Correctness

### 1. ~~Mixed commit strategies for same target repo~~ (RESOLVED)

**Priority:** High — **Status: Fixed**

~~When workflows with `direct` and `pull_request` strategies target the same destination repo, files are batched together and the last workflow's strategy silently wins.~~

**Resolution:** Implemented options (a) and (b):
- `UploadKey` now includes `CommitStrategy`, so files are naturally separated by strategy and produce independent write operations (direct commit vs. PR).
- A config-load-time warning alerts operators when workflows target the same `(repo, branch)` with different strategies.
- Write-phase log messages now include `strategy_source` and `file_count`.

**Related:** [Architecture > Target Repo Batching](ARCHITECTURE.md#5-target-repo-batching)

### 2. ~~Empty commits on duplicate processing~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~When two instances process the same webhook (e.g., local + Cloud Run), the second instance creates a commit with 0 file changes because the tree is already at HEAD.~~

**Resolution:** `createCommitTree` now returns the base commit's tree SHA alongside the new tree SHA. Both `addFilesToBranch` (direct path) and `commitFilesToBranch` (PR path) compare the two and skip the commit entirely when they match, logging `"Skipping empty commit — new tree is identical to HEAD tree"`.

**Files:** `services/github_write_to_target.go`

### 3. ~~PR title/body "last wins" is opaque~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~When multiple workflows batch into one PR, the commit message, PR title, and body come from whichever workflow ran last. There's no logging indicating which workflow's metadata was used.~~

**Resolution:** `workflow_processor.go` now logs when a subsequent workflow overwrites a batched commit message or PR title, including the previous and new values and the workflow name responsible.

## Reliability

### 4. Shared delivery tracking for multi-instance

**Priority:** Medium

The in-memory `DeliveryTracker` prevents duplicate processing within a single instance, but not across instances (e.g., local + Cloud Run, or multiple Cloud Run revisions during a deployment). Since MongoDB is already wired up for audit logging, add a `processed_deliveries` collection as an optional shared backend.

**Files:** `services/delivery_tracker.go`

### 5. ~~PR deduplication in target repos~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~If the app processes two source PRs in quick succession, it can create duplicate open PRs in the target repo.~~

**Resolution:** `addFilesViaPR` now calls `findExistingCopierPR` before creating a new branch. If an open PR from a `copier/*` branch targeting the same base branch exists, the app pushes new commits to that branch and updates the PR title/body instead of creating a duplicate.

### 6. ~~Graceful partial failure~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~If 3 workflows match but the 2nd fails mid-processing, the 3rd never runs.~~

**Resolution:** `processFilesWithWorkflows` now processes each workflow independently and returns a `map[string]error` of per-workflow failures. A failed workflow no longer blocks others. `handleMergedPRWithContainer` returns an aggregate error when any workflows fail, enabling the retry mechanism to re-attempt the full batch.

**Files:** `services/webhook_handler_new.go`

### 7. ~~Retry failed background processing~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~The webhook handler sends 200 OK immediately and processes in a background goroutine. If the goroutine fails, the webhook is lost.~~

**Resolution:** Implemented option (a) — `processWebhookWithRetry` wraps `handleMergedPRWithContainer` with exponential-backoff retries (configurable via `WEBHOOK_MAX_RETRIES`, default 2, and `WEBHOOK_RETRY_INITIAL_DELAY`, default 5s). Panics are converted to errors via `runWithRecovery`. After all retries are exhausted, a Slack alert is sent with the delivery context. Retries are skipped if the context deadline is exceeded.

**Files:** `services/webhook_handler_new.go`, `configs/environment.go`

### 8. Distinguish transient vs permanent errors

**Priority:** Low

API rate limits and network timeouts are retryable; 404 (repo not found) and 403 (no permission) are not. The retry logic in `rate_limit.go` handles rate limits, but other transient failures in the write path aren't retried.

**Files:** `services/rate_limit.go`, `services/github_write_to_target.go`

### 9. ~~Background processing timeout~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~The background goroutine that processes webhooks has no timeout. A stuck GitHub API call could leave it running indefinitely.~~

**Resolution:** The background goroutine now applies `context.WithTimeout` using the configurable `WEBHOOK_PROCESSING_TIMEOUT_SECONDS` env var (default 300s / 5 minutes). When the timeout fires, the context is cancelled, in-flight API calls abort, and the retry loop stops with a log and Slack alert. Set to 0 to disable the timeout.

**Files:** `services/webhook_handler_new.go`, `configs/environment.go`

## Security

### 10. ~~Rotate the test PEM key in `.env.test`~~ (RESOLVED)

**Priority:** High — **Status: Fixed**

~~`.env.test` contains a real (expired) base64-encoded private key.~~

**Resolution:** Replaced the old key with a purpose-generated 2048-bit RSA test-only key that was never associated with any GitHub App. Added the fingerprint to `.gitleaksignore` and a comment clarifying its test-only nature.

**Files:** `.env.test`, `.gitleaksignore`

### 11. ~~Tighten gosec exclusions~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~CI globally excludes `G703-G706` (taint analysis rules). These are broad.~~

**Resolution:** Removed all global `gosec` exclusions (`G115`, `G703`-`G706`) from the CI workflow. G703-G706 no longer fire (code changes and gosec updates eliminated them). The single remaining G115 hit (safe `int -> int32` for PR numbers) is suppressed with an inline `#nosec G115` comment. All 16 existing suppressions are now inline with explanations.

**Files:** `.github/workflows/ci.yml`, `services/github_read.go`

### 12. ~~Add `toolchain` directive to `go.mod`~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~The `go.mod` says `go 1.26.0` but nothing prevents contributors from building with an older toolchain.~~

**Resolution:** Added `toolchain go1.26.0` directive to `go.mod`. Contributors using Go 1.21+ will now automatically download and use the correct toolchain version, ensuring deterministic builds.

**Files:** `go.mod`

## Performance

### 13. ~~Cache resolved workflow configs~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~The app fetches all workflow configs from GitHub on every webhook (4+ API calls to resolve the main config and remote refs).~~

**Resolution:** Added `CachedConfigLoader` (decorator around `ConfigLoader`) with a configurable TTL (default 5 minutes, via `CONFIG_CACHE_TTL_SECONDS` env var). Repeated webhooks within the TTL window reuse the cached config without any GitHub API calls. Set to 0 to disable.

**Files:** `services/config_cache.go`, `services/service_container.go`, `configs/environment.go`

### 14. ~~Fetch file contents in parallel~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~`RetrieveFileContents()` is called sequentially for each matched file.~~

**Resolution:** Refactored `ProcessWorkflow` into three phases: match (sequential, fast), fetch (parallel via `errgroup` with a concurrency limit of 5), and queue (sequential, mutates shared state). File content fetches now run concurrently within each workflow, significantly reducing latency for PRs with many matched files.

**Files:** `services/workflow_processor.go`

### 15. ~~Handle GitHub API pagination for large PRs~~ (RESOLVED)

**Priority:** Medium — **Status: Already implemented**

~~Large PRs with 100+ changed files may require pagination in `GetFilesChangedInPr()`.~~

**Resolution:** `GetFilesChangedInPr()` already implements cursor-based pagination with `hasNextPage` and `first: 100` per page. No changes needed.

**Files:** `services/github_read.go`

## Code Quality

### 16. ~~Add a `.golangci.yml` config file~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~No linter config file exists; all settings are implicit defaults.~~

**Resolution:** Created `.golangci.yml` (v2 format) pinning enabled linters (`errcheck`, `govet`, `ineffassign`, `staticcheck`, `unused`, `misspell`, `revive`), formatters (`gofmt`, `goimports`), and documenting suppressed staticcheck rules (`ST1000`, `ST1003`, `SA1029`). CI and local pre-commit now share the same config.

**Files:** `.golangci.yml`

### 17. Split the `services/` package

**Priority:** Low

Everything lives in one package: auth, webhook handling, file processing, Slack, audit logging, config loading. Consider sub-packages like `services/github`, `services/config`, `services/notify` to reduce coupling and improve testability.

### 18. ~~Remove dead code paths~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~The legacy `ConfigLoader` (non-main-config path) and `CONFIG_FILE` env var are still present but unused in any real deployment. Deprecation-log them now, remove in a future release.~~

**Resolution:** Removed truly dead code from `config_loader.go`: `ValidateConfig`, `ConfigValidator`, `NewConfigValidator`, `ValidatePattern`, `TestPattern`, `TestTransform` (none were called anywhere). Removed unused `configLoader` field from `DefaultMainConfigLoader` in `main_config_loader.go`. Added deprecation warnings in `service_container.go` when the legacy single-file config path is used (`USE_MAIN_CONFIG=false`). Updated `cmd/config-validator/main.go` to call `PatternMatcher` and `PathTransformer` directly instead of through the removed `ConfigValidator` wrapper. The legacy `DefaultConfigLoader` and `CONFIG_FILE` env var are retained for backward compatibility but clearly marked as deprecated.

**Files:** `services/config_loader.go`, `services/main_config_loader.go`, `services/service_container.go`, `cmd/config-validator/main.go`

## Testing

### 19. ~~Integration test for target repo batching~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~Add a test that verifies correct behavior when multiple workflows target the same repo.~~

**Resolution:** Added `TestIntegration_TargetRepoBatching_MixedStrategies` which sends a webhook that triggers 3 workflows (2 direct + 1 PR) targeting the same repo. Verifies that the 2 direct workflows batch into 1 commit and the PR workflow produces a separate PR. Also updated the existing direct-commit integration test with the `MockGetCommit` mock for empty-commit detection.

**Files:** `services/integration_test.go`

### 20. ~~End-to-end webhook-to-commit test~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~There's no integration test that sends a webhook payload and verifies the resulting GitHub API calls (create tree, create commit, create PR).~~

**Resolution:** Multiple integration tests now cover this path:
- `TestIntegration_MergedPR_DirectCommit` — full webhook-to-direct-commit pipeline (config load, GraphQL file list, source fetch, tree/commit/ref update).
- `TestIntegration_TargetRepoBatching_MixedStrategies` — webhook-to-commit *and* webhook-to-PR, verifying batching and separate strategy handling.
- `TestIntegration_MergedPR_NoMatchingWorkflows` and `TestIntegration_MergedPR_ConfigLoadError` — error/edge case flows.

**Files:** `services/integration_test.go`

### 21. Contract tests for GitHub API responses

**Priority:** Low

Tests use `httpmock` with hardcoded response bodies. If GitHub changes their API response shape, tests still pass but production breaks. Consider recording real API responses as fixtures and replaying them.

### 22. Benchmarks for pattern matching

**Priority:** Low

If a config has many workflows with complex regex patterns, `ProcessWorkflow()` could become a bottleneck. Add `Benchmark` tests for the hot path to catch regressions.

**Files:** `services/workflow_processor_test.go`

## Operational

### 23. ~~Structured error alerting~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~Slack notifications exist but aren't connected to processing failures.~~

**Resolution:** `ErrorEvent` now includes `DeliveryID` and `Attempts` fields. `NotifyError` renders them as Slack message fields. `processWebhookWithRetry` threads the GitHub delivery ID through to the final Slack alert, providing full traceability from webhook receipt to failure notification.

**Files:** `services/slack_notifier.go`, `services/webhook_handler_new.go`

### 24. ~~Add a `/config` diagnostic endpoint~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~A read-only endpoint that shows the resolved effective config (all workflow configs resolved, defaults merged) without secrets. Useful for debugging "why didn't my workflow match?" without reading logs.~~

**Resolution:** Added a `GET /config` endpoint that returns a JSON diagnostic view containing: (a) a sanitised environment summary with all secret fields redacted to `[SET]`/`[NOT SET]`, and (b) the resolved workflow list (name, source/dest repos and branches, commit strategy, transform count, excludes). On config load failure the response includes a `load_error` field while still returning the environment section. Tests cover both the error path and a mock-injected workflow summary.

**Files:** `services/health_metrics.go`, `services/health_metrics_test.go`, `app.go`

### 25. Cloud Run `min-instances`

**Priority:** Low

Cold starts on Cloud Run mean the first webhook after idle takes extra time (Go binary startup + config resolution + GitHub auth). Setting `min-instances: 1` in the deploy config eliminates this at a small cost.

**Files:** `.github/workflows/ci.yml` (deploy step), `scripts/deploy-cloudrun.sh`

### 26. Per-workflow logging in write phase

**Priority:** Low

The processing phase logs which workflow matched each file, but the write phase (commit/PR creation) only logs the target repo. Add the workflow name(s) that contributed files to each commit/PR.

**Files:** `services/github_write_to_target.go`

## Documentation

### 27. ~~Add a CHANGELOG~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~There's no record of what changed between deployments.~~

**Resolution:** Created `CHANGELOG.md` in the repository root following the [Keep a Changelog](https://keepachangelog.com/) format. Documents all Added, Changed, Fixed, and Security items from the current development cycle with cross-references to recommendation numbers.

**Files:** `CHANGELOG.md`

### 28. ~~Config reference doc~~ (RESOLVED)

**Priority:** Medium — **Status: Fixed**

~~There's no single-page reference of every config option with types, defaults, and examples.~~

**Resolution:** Created `docs/CONFIG-REFERENCE.md` with two major sections: (1) all environment variables organized by category with types, defaults, and descriptions; (2) complete workflow YAML schema covering main config, workflow config, transformations, commit strategy, defaults, and `$ref` support.

**Files:** `docs/CONFIG-REFERENCE.md`

### 29. ~~Local testing: webhook routing~~ (RESOLVED)

**Priority:** Low — **Status: Fixed**

~~Document how to avoid the dual-delivery problem (local + Cloud Run both processing the same webhook).~~

**Resolution:** Added a "Webhook Routing: Avoiding Dual Delivery" section to `docs/LOCAL-TESTING.md` explaining why dual delivery happens and documenting four strategies: (1) swap the webhook URL, (2) local dry-run + Cloud Run live, (3) pause Cloud Run, (4) use a test-only source repository. Includes a quick decision guide table.

**Files:** `docs/LOCAL-TESTING.md`
