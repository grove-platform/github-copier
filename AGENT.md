# Agent Context: GitHub Copier

Webhook service: PR merged → match files → transform paths → copy to target repos.

## File Map

```
app.go                              # entrypoint, HTTP server, graceful shutdown
services/
  webhook_handler_new.go            # HandleWebhookWithContainer() orchestrator
  workflow_processor.go             # ProcessWorkflow() - core file matching logic
  pattern_matcher.go                # MatchFile(pattern, path) bool
  token_manager.go                  # TokenManager (thread-safe token state, sync.RWMutex)
  github_auth.go                    # ConfigurePermissions(), JWT generation
  github_read.go                    # GetFilesChangedInPr() (GraphQL), RetrieveFileContents()
  github_write_to_target.go         # AddFilesToTargetRepos(), addFilesViaPR()
  github_write_to_source.go         # UpdateDeprecationFile(filesToDeprecate)
  rate_limit.go                     # RateLimitTransport (auto-retry on 403/429)
  delivery_tracker.go               # DeliveryTracker (webhook idempotency via X-GitHub-Delivery)
  errors.go                         # Sentinel errors (ErrRateLimited, ErrNotFound, etc.)
  logger.go                         # slog JSON handler, LogCritical, LogAndReturnError
  file_state_service.go             # Tracks upload/deprecate queues (thread-safe)
  main_config_loader.go             # LoadConfig() with $ref support
  config_loader.go                  # Config loading & validation
  service_container.go              # DI container
  health_metrics.go                 # /health (liveness), /ready (readiness), /metrics
  audit_logger.go                   # MongoDB audit logging
  slack_notifier.go                 # Slack notifications
  pr_template_fetcher.go            # PR template resolution from target repos
types/
  config.go                         # Workflow, Transformation, SourcePattern structs
  types.go                          # ChangedFile, UploadKey, UploadFileContent
configs/environment.go              # Config struct, LoadEnvironment()
tests/utils.go                      # Test helpers, httpmock setup
cmd/
  config-validator/main.go          # CLI: validate configs, test patterns, init templates
  test-webhook/main.go              # CLI: send test webhook payloads (with delivery ID)
  test-pem/main.go                  # CLI: verify PEM key + App ID against GitHub API
scripts/
  ci-local.sh                       # Run full CI pipeline locally (build, test, lint, vet)
  run-local.sh                      # Run app locally with dev settings
  deploy-cloudrun.sh                # Deploy to Google Cloud Run
  integration-test.sh               # End-to-end integration test
```

## Key Types

```go
// types/config.go
type PatternType string    // "prefix" | "glob" | "regex"
type TransformationType string  // "move" | "copy" | "glob" | "regex"

type Workflow struct {
    Name             string
    Source           Source              // Repo, Branch, InstallationID
    Destination      Destination         // Repo, Branch
    Transformations  []Transformation    // Type, From, To, Pattern, Replacement
    Exclude          []string
    CommitStrategy   *CommitStrategyConfig // Type (direct|pull_request), PRTitle, PRBody, AutoMerge
    DeprecationCheck *DeprecationConfig
}

// types/types.go
type ChangedFile struct { Path, Status string }  // Status: "ADDED"|"MODIFIED"|"DELETED"
type UploadKey struct { RepoName, BranchPath string }
```

## State Management

All mutable state is encapsulated in `TokenManager` (thread-safe via `sync.RWMutex`):
- Installation access token
- Per-org installation tokens with expiry
- Cached JWT
- HTTP client

Per-request file state is managed via `FileStateService` in the `ServiceContainer`.

Webhook idempotency is handled by `DeliveryTracker` (TTL-based, in-memory).

## Target Repo Batching

Multiple workflows targeting the **same destination repo** are batched into a single commit/PR.
The last workflow's commit strategy, PR title/body, and auto-merge setting wins.
To get separate PRs per workflow, use different destination repos or branches.
See `docs/ARCHITECTURE.md` § "Target Repo Batching" for full details.

## Config Example

```yaml
workflows:
  - name: "sync-docs"
    source: { repo: "org/src", branch: "main", patterns: [{type: glob, pattern: "docs/**"}] }
    destination: { repo: "org/dest", branch: "main" }
    transformations: [{ type: move, from: "docs/", to: "public/" }]
    commit_strategy: { type: pull_request, pr_title: "Sync docs" }  # type: direct|pull_request
```

## Test Commands

```bash
go test -race ./...                              # all with race detector
go test ./services/... -run TestWorkflow -v      # specific
golangci-lint run ./...                          # lint
```

## Edit Patterns

| Task | Files to modify |
|------|-----------------|
| New transformation | `types/config.go` (TransformationType) → `workflow_processor.go` (processFileForWorkflow) |
| New pattern type | `types/config.go` (PatternType) → `pattern_matcher.go` |
| New config field | `types/config.go` (struct) → consumers in `workflow_processor.go` |
| Webhook logic | `webhook_handler_new.go` |
| Rate limit behavior | `rate_limit.go` |
| Auth flow | `github_auth.go` + `token_manager.go` |
| CLI tool | `cmd/<tool>/main.go` + `cmd/<tool>/README.md` |

## Conventions

- Return `error`, never `log.Fatal`
- Wrap errors: `fmt.Errorf("context: %w", err)`
- Use sentinel errors from `errors.go` where appropriate
- Nil-check GitHub API responses before dereference
- Use `log/slog` for all logging (never `log` or `fmt.Print` for operational output)
- Tests use `httpmock`; see `tests/utils.go`
- Always run tests with `-race` flag
- **Changelog**: Update `CHANGELOG.md` for all notable changes (follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/))
