# Agent Context: GitHub Copier

Webhook service: PR merged → match files → transform paths → copy to target repos.

## File Map

```
app.go                              # entrypoint, HTTP server
services/
  webhook_handler_new.go            # HandleWebhookWithContainer()
  workflow_processor.go             # ProcessWorkflow() - core logic
  pattern_matcher.go                # MatchFile(pattern, path) bool
  github_auth.go                    # ConfigurePermissions() error
  github_read.go                    # GetFilesChangedInPr(), RetrieveFileContents()
  github_write_to_target.go         # AddFilesToTargetRepoBranch()
  github_write_to_source.go         # UpdateDeprecationFile()
  file_state_service.go             # tracks upload/deprecate queues
  main_config_loader.go             # LoadConfig() with $ref support
  service_container.go              # DI container
types/
  config.go                         # Workflow, Transformation, SourcePattern structs
  types.go                          # ChangedFile, UploadKey, UploadFileContent
configs/environment.go              # Config struct, LoadEnvironment()
tests/utils.go                      # test helpers, httpmock setup
```

## Key Types

```go
// types/config.go
type PatternType string    // "prefix" | "glob" | "regex"
type TransformationType string  // "move" | "copy" | "glob" | "regex"

type Workflow struct {
    Name           string
    Source         SourceConfig      // Repo, Branch, Patterns []SourcePattern
    Destination    DestinationConfig // Repo, Branch
    Transformations []Transformation // Type, From, To, Pattern, Replacement
    Commit         CommitConfig      // Strategy, Message, PRTitle, AutoMerge
}

// types/types.go
type ChangedFile struct { Path, Status string }  // Status: "ADDED"|"MODIFIED"|"DELETED"
type UploadKey struct { RepoName, BranchPath string }
```

## Global State (⚠️ mutable)

```go
// services/github_write_to_target.go
var FilesToUpload map[UploadKey]UploadFileContent
// services/github_auth.go
var InstallationAccessToken string
var OrgTokens map[string]string
```

## Config Example

```yaml
workflows:
  - name: "sync-docs"
    source: { repo: "org/src", branch: "main", patterns: [{type: glob, pattern: "docs/**"}] }
    destination: { repo: "org/dest", branch: "main" }
    transformations: [{ type: move, from: "docs/", to: "public/" }]
    commit: { strategy: pr, message: "Sync" }  # strategy: direct|pr
```

## Test Commands

```bash
go test ./...                                    # all
go test ./services/... -run TestWorkflow -v      # specific
```

## Edit Patterns

| Task | Files to modify |
|------|-----------------|
| New transformation | `types/config.go` (TransformationType) → `workflow_processor.go` (processFileForWorkflow) |
| New pattern type | `types/config.go` (PatternType) → `pattern_matcher.go` |
| New config field | `types/config.go` (struct) → consumers in `workflow_processor.go` |
| Webhook logic | `webhook_handler_new.go` |

## Conventions

- Return `error`, never `log.Fatal`
- Wrap errors: `fmt.Errorf("context: %w", err)`
- Nil-check GitHub API responses before dereference
- Tests use `httpmock`; see `tests/utils.go`
- **Changelog**: Update `CHANGELOG.md` for all notable changes (follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/))
