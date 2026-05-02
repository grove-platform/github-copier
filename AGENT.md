# Agent Context: GitHub Copier

Webhook service + operator UI.

**Webhook pipeline:** PR merged → match files → transform paths → copy to target repos.
**Operator UI:** `/operator/` — diagnostic dashboard with PAT auth, replay, audit browsing, and an AI rule suggester. Enabled via `OPERATOR_UI_ENABLED=true` + `OPERATOR_AUTH_REPO`.

## File Map

```
app.go                              # Entrypoint, HTTP server, graceful shutdown, startup banner
services/
  # Webhook pipeline
  webhook_handler_new.go            # HandleWebhookWithContainer() — orchestrator
  workflow_processor.go             # ProcessWorkflow() — core file matching logic
  pattern_matcher.go                # MatchFile(pattern, path) — prefix/glob/regex
  github_auth.go                    # ConfigurePermissions(), JWT generation, LoadWebhookSecret, LoadMongoURI, LoadAnthropicAPIKey
  github_read.go                    # GetFilesChangedInPr() (GraphQL), RetrieveFileContents()
  github_write_to_target.go         # AddFilesToTargetRepos(); errTreeUnchanged sentinel for empty commits
  github_write_to_source.go         # UpdateDeprecationFile(filesToDeprecate)
  token_manager.go                  # TokenManager (thread-safe install tokens, sync.RWMutex)
  rate_limit.go                     # RateLimitTransport (auto-retry on 403/429)
  delivery_tracker.go               # Webhook idempotency via X-GitHub-Delivery
  file_state_service.go             # Per-request upload/deprecate queues (thread-safe)
  errors.go                         # Sentinel errors (ErrRateLimited, ErrNotFound, etc.)
  logger.go                         # slog JSON handler, LogCritical, LogAndReturnError
  main_config_loader.go             # LoadConfig() with $ref support
  config_loader.go                  # Config loading & validation
  config_cache.go                   # CachedConfigLoader (TTL-based)
  service_container.go              # DI container
  health_metrics.go                 # /health, /ready, /metrics, /config
  audit_logger.go                   # MongoDB audit logging (driver v2; ObjectIDAsHexString for read decoding)
  slack_notifier.go                 # Slack notifications
  pr_template_fetcher.go            # PR template resolution from target repos
  webhook_trace_buffer.go           # Ring buffer of recent webhook traces (Overview/Webhooks tabs)
  log_buffer.go                     # Context-tagged per-delivery log ring buffer (logs drawer)

  # Operator UI
  operator_ui.go                    # RegisterOperatorRoutes, wrapAPI / wrapOperatorOnly middleware,
                                    #   handleMe, handleRepoPermission, handleDeployment, handleReplay,
                                    #   handleRelease, githubCreateVersionTag, sharedGithubHTTPClient,
                                    #   llmPingCache, ReleaseAPIMode enum
  operator_auth.go                  # GitHub PAT validation; ghAuthCache (SHA-256 hashed keys);
                                    #   validateGitHubPAT role mapping; ghAPIError (StatusCode,
                                    #   IsTransient); 5xx = soft-fail to writer, else RoleDenied
  operator_ratelimit.go             # tokenBucket — fixed-window rate limiter keyed by hashed PAT
                                    #   (30/hour on /suggest-rule)
  operator_suggest_rule.go          # AI rule suggester; SuggestRuleSystemPrompt (exported);
                                    #   verifySuggestedRule (runs rule through PatternMatcher)
  operator_llm_admin.go             # /llm/status (cached 30s), /llm/settings, /llm/pull (NDJSON),
                                    #   /llm/model delete. Maps ErrModelManagementNotSupported to 400.
  llm_client.go                     # LLMClient interface, NewLLMClient(LLMClientOptions) dispatch,
                                    #   ErrModelManagementNotSupported, ollamaClient impl
  llm_anthropic.go                  # anthropicClient — /v1/messages, /v1/models, dual x-api-key +
                                    #   api-key headers (native API + Azure APIM gateway support)
  web/operator/index.html           # Embedded single-file SPA (HTML + CSS + JS); served by serveIndex

types/
  config.go                         # Workflow, Transformation, SourcePattern, CommitStrategyConfig
  types.go                          # ChangedFile, UploadKey, UploadFileContent
configs/environment.go              # Config struct, LoadEnvironment(), validateOperatorAuth (hard-fail
                                    #   when UI enabled without auth repo), per-provider LLM defaults
cmd/
  config-validator/                 # CLI: validate configs, test patterns, init templates
  test-webhook/                     # CLI: send test webhook payloads (with delivery ID)
  test-pem/                         # CLI: verify PEM key + App ID against GitHub API
  test-llm/                         # CLI: smoke-test LLM provider end-to-end (Ping, ListModels,
                                    #   GenerateJSON with the real SuggestRuleSystemPrompt)
scripts/
  ci-local.sh                       # Run full CI pipeline locally
  run-local.sh                      # Run app locally with dev settings
  deploy-cloudrun.sh                # Deploy to Google Cloud Run (manual fallback)
  integration-test.sh               # End-to-end integration test
  release.sh                        # Create versioned release (tag, CHANGELOG, GitHub Release)
  test-slack.sh                     # Test Slack notification integration
  diagnose-github-auth.sh           # Debug GitHub App authentication issues
```

## Key Types

```go
// types/config.go
type PatternType string              // "prefix" | "glob" | "regex"
type TransformationType string       // "move" | "copy" | "glob" | "regex"

type Workflow struct {
    Name             string
    Source           Source                // Repo, Branch, InstallationID
    Destination      Destination           // Repo, Branch
    Transformations  []Transformation      // Type, From, To, Pattern, Replacement
    Exclude          []string
    CommitStrategy   *CommitStrategyConfig // Type, PRTitle, PRBody, AutoMerge
    DeprecationCheck *DeprecationConfig
}

// services/llm_client.go
type LLMClient interface {
    GenerateJSON(ctx, system, user string) (string, error)
    ProviderName() string
    Ping(ctx) error
    Get/SetBaseURL, Get/SetActiveModel
    ListModels(ctx) ([]LLMModel, error)
    PullModel(ctx, name, progressFn) error       // ollama only
    DeleteModel(ctx, name) error                  // ollama only
}
type LLMClientOptions struct { Provider, BaseURL, Model, APIKey string }

// services/operator_auth.go
type OperatorRole string              // "operator" | "writer" | "denied"
type ghAPIError struct { StatusCode int; Body string }  // exposes IsTransient()
```

## State Management

- **Per-install tokens**: `TokenManager` (thread-safe via `sync.RWMutex`), cached JWT, HTTP client.
- **Per-request file state**: `FileStateService` on the `ServiceContainer`.
- **Webhook idempotency**: `DeliveryTracker` (TTL-based, in-memory).
- **PAT auth cache**: `ghAuthCache` (5-min TTL). Keys are **SHA-256 hashes** of the PAT — raw tokens never sit in the heap. Stores the full `*OperatorUser` and per-repo permission levels.
- **LLM settings**: process-global, in-memory, mutated at runtime via `/llm/settings`. Revert to env defaults on restart; the UI hint calls this out.
- **LLM ping cache**: 30s TTL; invalidated on `SetBaseURL` / `SetActiveModel`.
- **Rate limit buckets**: fixed-window (30/hour) on `/suggest-rule`, keyed by hashed PAT. Opportunistic eviction.
- **Log buffer**: context-tagged ring buffer (`ContextWithLogBuffer`) captures slog output per webhook delivery for the logs drawer.

## Authorization Model (Operator UI)

Each user signs in with their own GitHub PAT. Permission on `OPERATOR_AUTH_REPO` decides role:

| GitHub permission | Role       | Capabilities                                              |
|-------------------|------------|-----------------------------------------------------------|
| `admin`, `maintain`| operator  | All UI, replay, release, AI settings                      |
| `write`, `triage`, `read` | writer | View audit/workflows/copies, AI rule suggester        |
| none              | denied    | 401                                                        |

`write` is deliberately **not** operator — docs contributors typically have `write` on the auth repo and shouldn't get replay/release capability.

Additional gate on replay: user's PAT must have read access to the **source repo of the webhook being replayed** (checked via `ghAuthCache.CanUserReadRepo`).

Permission-check error handling: **5xx from GitHub is soft-failed to writer** (transient outage shouldn't lock everyone out); everything else (404, 401, 403, network, parse error) → `RoleDenied`. The distinction is carried by `ghAPIError.IsTransient()`.

## Target Repo Batching

Multiple workflows targeting the same destination repo are batched into a single commit/PR. The last workflow's commit strategy, PR title/body, and auto-merge setting wins. See `docs/ARCHITECTURE.md` § "Target Repo Batching".

## Config Example

```yaml
workflows:
  - name: "sync-docs"
    source: { repo: "org/src", branch: "main", patterns: [{type: glob, pattern: "docs/**"}] }
    destination: { repo: "org/dest", branch: "main" }
    transformations: [{ type: move, from: "docs/", to: "public/" }]
    commit_strategy: { type: pull_request, pr_title: "Sync docs" }
```

## Quick Reference

```bash
# Build & Run
make build                                       # build binary
make run                                         # run with .env
./github-copier -env .env.test                   # run with specific env file

# Testing
go test -race ./...                              # all tests with race detector
go test ./services/ -run TestValidateGitHubPAT -v  # specific test

# Linting + security
golangci-lint run ./...                          # lint (.golangci.yml)
gosec ./...                                      # security scanner; should be 0 issues

# CI (local)
./scripts/ci-local.sh                            # full CI: build, test, lint, vet

# Release
./scripts/release.sh v1.2.3 --dry-run            # preview
./scripts/release.sh v1.2.3                      # tag + push, triggers Cloud Run deploy

# Operator UI smoke test
go build -o test-llm ./cmd/test-llm && ./test-llm -env .env.test
```

## Release Process

Semantic versioning (`vMAJOR.MINOR.PATCH`) via `scripts/release.sh`. Prereqs: clean `main`, `gh` authed, `[Unreleased]` populated. The script promotes `[Unreleased]` to a dated heading, commits, tags, pushes — the tag push triggers the Cloud Run deploy in `.github/workflows/ci.yml`. See the `## Release` section of `README.md` for detail.

**Changelog**: Follow [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Sections: `Added`, `Changed`, `Fixed`, `Security`, `Deprecated`, `Removed`.

## Edit Patterns

| Task                                   | Files to modify |
|----------------------------------------|-----------------|
| New transformation type                | `types/config.go` (TransformationType) → `workflow_processor.go` (processFileForWorkflow) |
| New pattern type                       | `types/config.go` (PatternType) → `pattern_matcher.go` |
| New config field                       | `types/config.go` → consumers in `workflow_processor.go` |
| New env var                            | `configs/environment.go` (field + const + loader); update `docs/CONFIG-REFERENCE.md` |
| Webhook pipeline logic                 | `webhook_handler_new.go` → `workflow_processor.go` |
| Rate-limit behavior (GitHub API)       | `rate_limit.go` |
| Auth flow (App)                        | `github_auth.go` + `token_manager.go` |
| Operator UI route                      | `operator_ui.go` (RegisterOperatorRoutes + handler) + `services/web/operator/index.html` |
| Operator UI auth / role                | `operator_auth.go` (role mapping, `ghAPIError`, cache) |
| LLM provider                           | Implement `LLMClient` in new `llm_<provider>.go`; dispatch in `llm_client.go` `NewLLMClient` |
| LLM prompt change                      | `operator_suggest_rule.go` (`SuggestRuleSystemPrompt`); rerun `cmd/test-llm` to validate |
| AI suggester UI change                 | `services/web/operator/index.html` §§ `ai-settings` / `ai-suggester` |
| CLI tool                               | `cmd/<tool>/main.go` + `cmd/<tool>/README.md` |

## Conventions

- Return `error`, never `log.Fatal`. Wrap with `fmt.Errorf("context: %w", err)`.
- Sentinel errors from `errors.go`; new sentinels go next to the function that owns them (e.g. `ErrModelManagementNotSupported` in `llm_client.go`, `errTreeUnchanged` in `github_write_to_target.go`).
- Nil-check GitHub API responses before dereferencing.
- All logging via `log/slog`. Never `log.*` or `fmt.Print*` for operational output.
- Tests use `httpmock` (see `tests/utils.go`) for webhook flow; `httptest.Server` with `githubAPIBaseURL` package var override for operator auth tests.
- Always run tests with `-race`.
- **gosec must stay clean.** New HTTP URLs go through `githubAPIBaseURL` (or the equivalent Anthropic base URL) with validated path components + `url.PathEscape`, not raw user input. Document each `#nosec` inline.
- **Secrets never get logged or embedded in paths.** Use `hashToken` when you need a stable identifier derived from a PAT.
- **CHANGELOG.md**: update `[Unreleased]` for all notable changes.

## Security Posture (recap)

Details that tripped previous reviews:

- **Auth failure ≠ writer role**: only transient 5xx from the GitHub permission check keeps the default writer role. Every other failure → `RoleDenied`.
- **No raw PATs in heap beyond request scope**: `ghAuthCache` keys on `hashToken(pat)`. Memory dumps can't leak active tokens.
- **LLM cost cap**: `/suggest-rule` is 30/hour per hashed-PAT; `/llm/status` ping is cached 30s.
- **SSRF defense-in-depth**: all GitHub API paths validate owner/repo/branch against RE2 whitelists (`ghUsernameRe`, `ghRepoNameRe`, `ghBranchNameRe`) and use `url.PathEscape` before embedding.

## Key Documentation

| Doc | Purpose |
|-----|---------|
| [`README.md`](README.md) | Feature overview, quick start, operator UI + AI suggester |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | System design, data flow, batching behavior |
| [`docs/CONFIG-REFERENCE.md`](docs/CONFIG-REFERENCE.md) | Full env-var + YAML schema reference |
| [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) | Cloud Run deployment, Secret Manager setup |
| [`docs/LOCAL-TESTING.md`](docs/LOCAL-TESTING.md) | Running and testing locally (incl. operator UI) |
| [`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md) | Common issues and debugging |
| [`docs/FAQ.md`](docs/FAQ.md) | FAQ including operator UI / AI suggester |
| [`cmd/test-llm/README.md`](cmd/test-llm/README.md) | LLM provider smoke test |
| [`testdata/README.md`](testdata/README.md) | Test fixtures and webhook payload examples |
