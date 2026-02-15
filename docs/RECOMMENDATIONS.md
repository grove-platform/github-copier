# Recommendations & Backlog

Improvement recommendations for the github-copier application, organized by category and priority.

Last updated: 2026-02-15

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

### 6. Graceful partial failure

**Priority:** Medium

If 3 workflows match but the 2nd fails mid-processing (e.g., API error creating a branch), the 1st workflow's files are already committed but the 3rd never runs. Consider processing all workflows independently and reporting per-workflow success/failure.

**Files:** `services/webhook_handler_new.go`

### 7. Retry failed background processing

**Priority:** Medium

The webhook handler sends 200 OK immediately and processes in a background goroutine. If the goroutine fails (network error, transient API failure), the webhook is lost — GitHub already got a 200 OK so it won't retry.

**Options:**
- (a) Simple in-memory retry queue with backoff
- (b) Write failed events to MongoDB and re-process on a schedule

**Files:** `services/webhook_handler_new.go`

### 8. Distinguish transient vs permanent errors

**Priority:** Low

API rate limits and network timeouts are retryable; 404 (repo not found) and 403 (no permission) are not. The retry logic in `rate_limit.go` handles rate limits, but other transient failures in the write path aren't retried.

**Files:** `services/rate_limit.go`, `services/github_write_to_target.go`

### 9. Background processing timeout

**Priority:** Medium

The background goroutine that processes webhooks has no timeout. A stuck GitHub API call could leave it running indefinitely. Add a `context.WithTimeout` on the background work.

**Files:** `services/webhook_handler_new.go`

## Security

### 10. Rotate the test PEM key in `.env.test`

**Priority:** High

`.env.test` contains a real (expired) base64-encoded private key. Replace it with a purpose-generated test-only key that was never associated with a real GitHub App. Even expired keys in repos are a red flag for auditors.

**Files:** `.env.test`

### 11. Tighten gosec exclusions

**Priority:** Low

CI globally excludes `G703-G706` (taint analysis rules). These are broad. Consider using inline `#nosec G704` comments on specific lines instead, so new code gets checked by default.

**Files:** `.github/workflows/ci.yml`, affected source files

### 12. Add `toolchain` directive to `go.mod`

**Priority:** Low

The `go.mod` says `go 1.26.0` but nothing prevents contributors from building with an older toolchain. Add `toolchain go1.26.0` (Go 1.21+ feature) for deterministic builds.

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

### 16. Add a `.golangci.yml` config file

**Priority:** Medium

No linter config file exists; all settings are implicit defaults. A config file would pin enabled linters, set severity levels, configure per-linter options, and document why certain checks are disabled. Keeps CI and local `pre-commit` behavior consistent.

### 17. Split the `services/` package

**Priority:** Low

Everything lives in one package: auth, webhook handling, file processing, Slack, audit logging, config loading. Consider sub-packages like `services/github`, `services/config`, `services/notify` to reduce coupling and improve testability.

### 18. Remove dead code paths

**Priority:** Low

The legacy `ConfigLoader` (non-main-config path) and `CONFIG_FILE` env var are still present but unused in any real deployment. Deprecation-log them now, remove in a future release.

**Files:** `configs/environment.go`, `services/config_loader.go`, `services/service_container.go`

## Testing

### 19. Integration test for target repo batching

**Priority:** Medium

Add a test that verifies correct behavior when multiple workflows target the same repo: file combination, strategy selection, and commit message sourcing.

**Files:** `services/github_write_to_target_test.go`

### 20. End-to-end webhook-to-commit test

**Priority:** Medium

There's no integration test that sends a webhook payload and verifies the resulting GitHub API calls (create tree, create commit, create PR). This is the most critical path and is only tested implicitly.

**Files:** `services/integration_test.go`

### 21. Contract tests for GitHub API responses

**Priority:** Low

Tests use `httpmock` with hardcoded response bodies. If GitHub changes their API response shape, tests still pass but production breaks. Consider recording real API responses as fixtures and replaying them.

### 22. Benchmarks for pattern matching

**Priority:** Low

If a config has many workflows with complex regex patterns, `ProcessWorkflow()` could become a bottleneck. Add `Benchmark` tests for the hot path to catch regressions.

**Files:** `services/workflow_processor_test.go`

## Operational

### 23. Structured error alerting

**Priority:** Medium

Slack notifications exist but aren't connected to processing failures. Add a Slack alert when a webhook fails to process (after retries), including the delivery ID and error context.

**Files:** `services/slack_notifier.go`, `services/webhook_handler_new.go`

### 24. Add a `/config` diagnostic endpoint

**Priority:** Low

A read-only endpoint that shows the resolved effective config (all workflow configs resolved, defaults merged) without secrets. Useful for debugging "why didn't my workflow match?" without reading logs.

**Files:** `app.go`, `services/main_config_loader.go`

### 25. Cloud Run `min-instances`

**Priority:** Low

Cold starts on Cloud Run mean the first webhook after idle takes extra time (Go binary startup + config resolution + GitHub auth). Setting `min-instances: 1` in the deploy config eliminates this at a small cost.

**Files:** `.github/workflows/ci.yml` (deploy step), `scripts/deploy-cloudrun.sh`

### 26. Per-workflow logging in write phase

**Priority:** Low

The processing phase logs which workflow matched each file, but the write phase (commit/PR creation) only logs the target repo. Add the workflow name(s) that contributed files to each commit/PR.

**Files:** `services/github_write_to_target.go`

## Documentation

### 27. Add a CHANGELOG

**Priority:** Medium

There's no record of what changed between deployments. A `CHANGELOG.md` or GitHub Releases workflow would help track changes across versions.

### 28. Config reference doc

**Priority:** Medium

There's no single-page reference of every config option with types, defaults, and examples. The info is scattered across ARCHITECTURE.md, FAQ.md, and inline comments. A dedicated `docs/CONFIG-REFERENCE.md` would save time for anyone writing a new workflow config.

### 29. Local testing: webhook routing

**Priority:** Low

Document how to avoid the dual-delivery problem (local + Cloud Run both processing the same webhook). Recommend pointing the GitHub App webhook URL at smee during local testing, or using dry-run locally and letting Cloud Run handle real processing.

**Files:** `docs/LOCAL-TESTING.md`, `docs/WEBHOOK-TESTING.md`
