# Agent Context: GitHub Copier

Webhook service: PR merged → match files → transform paths → copy to target repos.

## File Map

```
app.go                              # Entrypoint, HTTP server, graceful shutdown
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
  config_cache.go                   # CachedConfigLoader (TTL-based config caching)
  service_container.go              # DI container
  health_metrics.go                 # /health (liveness), /ready (readiness), /metrics, /config
  audit_logger.go                   # MongoDB audit logging
  slack_notifier.go                 # Slack notifications
  pr_template_fetcher.go            # PR template resolution from target repos
  integration_test.go               # End-to-end integration tests
types/
  config.go                         # Workflow, Transformation, SourcePattern, MainConfig structs
  types.go                          # ChangedFile, UploadKey, UploadFileContent, CommitStrategy
configs/environment.go              # Config struct, LoadEnvironment()
tests/utils.go                      # Test helpers, httpmock setup
cmd/
  config-validator/                 # CLI: validate configs, test patterns, init templates
  test-webhook/                     # CLI: send test webhook payloads (with delivery ID)
  test-pem/                         # CLI: verify PEM key + App ID against GitHub API
scripts/
  ci-local.sh                       # Run full CI pipeline locally (build, test, lint, vet)
  run-local.sh                      # Run app locally with dev settings
  deploy-cloudrun.sh                # Deploy to Google Cloud Run
  integration-test.sh               # End-to-end integration test runner
  release.sh                        # Create versioned release (tag, changelog, GitHub Release)
  test-slack.sh                     # Test Slack notification integration
  test-with-pr.sh                   # Test webhook with a real PR
  test-github-access.sh             # Verify GitHub API access
  diagnose-github-auth.sh           # Debug GitHub App authentication issues
  check-installation-repos.sh       # List repos accessible to GitHub App installation
  grant-secret-access.sh            # Grant service account access to GCP secrets
```

## Key Types

```go
// types/config.go
type PatternType string         // "prefix" | "glob" | "regex"
type TransformationType string  // "move" | "copy" | "glob" | "regex"

type Workflow struct {
    Name             string
    Source           Source              // Repo, Branch, Patterns, InstallationID
    Destination      Destination         // Repo, Branch
    Transformations  []Transformation    // Type, From, To, Pattern, Replacement
    Exclude          []string
    CommitStrategy   *CommitStrategyConfig // Type (direct|pull_request), PRTitle, PRBody, AutoMerge
    DeprecationCheck *DeprecationConfig
}

type MainConfig struct {
    Defaults        *Defaults           // Global defaults for all workflows
    WorkflowConfigs []WorkflowConfigRef // References to workflow config files
}

// types/types.go
type ChangedFile struct { Path, Status string }  // Status: "ADDED"|"MODIFIED"|"DELETED"
type UploadKey struct {
    RepoName       string
    BranchPath     string
    RuleName       string   // Allows multiple rules targeting same repo/branch
    CommitStrategy string   // Differentiates direct vs PR for batching
}
type CommitStrategy string  // "direct" | "pull_request"
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

## Quick Reference

```bash
# Build & Run
make build                                       # build binary
make run                                         # run with .env
./github-copier -env .env.test                   # run with specific env file

# Testing
go test -race ./...                              # all tests with race detector
go test ./services/... -run TestWorkflow -v      # specific test
make test                                        # run all tests via Makefile

# Linting
golangci-lint run ./...                          # lint (config: .golangci.yml)
make lint                                        # lint via Makefile

# CI (local)
./scripts/ci-local.sh                            # full CI: build, test, lint, vet

# Release
./scripts/release.sh v1.2.3                      # create release (see below)
./scripts/release.sh v1.2.3 --dry-run            # preview without changes
```

## Release Process

Releases use semantic versioning (`vMAJOR.MINOR.PATCH`) and are automated via `scripts/release.sh`.

**Prerequisites:**
- Clean working tree on `main` branch
- `gh` CLI authenticated
- `CHANGELOG.md` has content in `[Unreleased]` section

**What the script does:**
1. Validates version format and prerequisites
2. Renames `[Unreleased]` → `[vX.Y.Z] - YYYY-MM-DD` in `CHANGELOG.md`
3. Adds fresh `[Unreleased]` section
4. Commits: `Release vX.Y.Z`
5. Creates annotated git tag
6. Pushes to origin (triggers CI deploy via `v*` tag)
7. Creates GitHub Release with changelog excerpt

**Changelog convention:**
- Follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) format
- Sections: `Added`, `Changed`, `Fixed`, `Security`, `Deprecated`, `Removed`
- Add entries to `[Unreleased]` as you work; the release script promotes them

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
- Use `github.Ptr()` (not deprecated `github.String`/`github.Int`/`github.Bool`)
- Use `log/slog` for all logging (never `log` or `fmt.Print` for operational output)
- Tests use `httpmock`; see `tests/utils.go`
- Always run tests with `-race` flag
- **Changelog**: Update `CHANGELOG.md` under `[Unreleased]` for all notable changes (follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/))

## Key Documentation

| Doc | Purpose |
|-----|---------|
| `docs/ARCHITECTURE.md` | System design, data flow, batching behavior |
| `docs/CONFIG-REFERENCE.md` | Full config schema and field reference |
| `docs/DEPLOYMENT.md` | Cloud Run deployment, secrets setup, rollback |
| `docs/TROUBLESHOOTING.md` | Common issues and debugging |
| `docs/LOCAL-TESTING.md` | Running and testing locally |
| `docs/PATTERN-MATCHING-GUIDE.md` | Pattern syntax with examples |
| `docs/SLACK-NOTIFICATIONS.md` | Slack integration setup |
| `docs/WEBHOOK-TESTING.md` | Testing webhooks locally and in prod |
| `testdata/README.md` | Test fixtures and webhook payload examples |
| `scripts/README.md` | Script usage documentation |

## Cursor Rules & Skills

Project-specific AI assistance is configured in `.cursor/`:

```
.cursor/
  rules/
    go-conventions.mdc      # Go coding standards (error handling, logging, testing)
    edit-patterns.mdc       # Task → file mapping reference
  skills/
    test-workflow-config/   # Validate configs, test patterns/transforms
    check-webhook-delivery/ # Verify webhook success, check logs
```
