# Agent Context: GitHub Copier

## Purpose
GitHub webhook service that copies files between repositories when PRs are merged. Listens for `pull_request` events, applies transformation rules, and syncs files to target repos.

## Architecture

```
app.go (entrypoint)
  └── ServiceContainer (DI container)
        ├── ConfigLoader         → loads workflow configs (YAML)
        ├── PatternMatcher       → matches files via prefix/glob/regex
        ├── PathTransformer      → transforms source→target paths
        ├── WorkflowProcessor    → orchestrates file processing
        ├── FileStateService     → tracks files to upload/deprecate
        ├── MetricsCollector     → health/metrics endpoints
        ├── AuditLogger          → MongoDB audit trail
        └── SlackNotifier        → notifications
```

## Key Files

| File | Purpose |
|------|---------|
| `app.go` | HTTP server, webhook routing |
| `services/webhook_handler_new.go` | Webhook validation, PR event handling |
| `services/workflow_processor.go` | Core logic: match files → transform paths → queue uploads |
| `services/pattern_matcher.go` | Pattern matching (prefix, glob, regex) |
| `services/github_auth.go` | GitHub App JWT auth, installation tokens |
| `services/github_read.go` | Read files/PRs from GitHub |
| `services/github_write_to_target.go` | Create branches, commits, PRs on target repos |
| `services/github_write_to_source.go` | Update deprecation files in source repo |
| `services/main_config_loader.go` | Load configs with `$ref` support |
| `types/config.go` | All config structs (Workflow, Transformation, etc.) |
| `types/types.go` | API types (ChangedFile, UploadKey, etc.) |
| `configs/environment.go` | Environment config loading |

## Data Flow

```
1. GitHub webhook (PR merged) → webhook_handler_new.go
2. Load workflow configs → config_loader.go / main_config_loader.go
3. Get changed files from PR → github_read.go
4. For each workflow:
   a. Match files against source patterns → pattern_matcher.go
   b. Apply transformations (move/copy/glob/regex) → workflow_processor.go
   c. Queue files for upload → file_state_service.go
5. Upload files to target repos → github_write_to_target.go
6. Update deprecation files → github_write_to_source.go
```

## Config Structure

```yaml
workflows:
  - name: "sync-docs"
    source:
      repo: "org/source-repo"
      branch: "main"
      patterns:
        - type: glob          # prefix | glob | regex
          pattern: "docs/**"
    destination:
      repo: "org/target-repo"
      branch: "main"
    transformations:
      - type: move            # move | copy | glob | regex
        from: "docs/"
        to: "public/docs/"
    commit:
      strategy: pr            # direct | pr
      message: "Sync docs"
```

## Global State (⚠️ Legacy)

These package-level vars in `services/` are used for file uploads:
- `FilesToUpload map[UploadKey]UploadFileContent` - queued files
- `InstallationAccessToken string` - cached GitHub token
- `OrgTokens map[string]string` - per-org token cache

## Testing

```bash
go test ./...                           # all tests
go test ./services/... -v               # services with verbose
go test -run TestWorkflowProcessor ./services/...  # specific test
```

Mock HTTP with `httpmock` package. See `tests/utils.go` for helpers.

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `GITHUB_APP_ID` | GitHub App ID |
| `GITHUB_INSTALLATION_ID` | Installation ID |
| `GITHUB_PRIVATE_KEY_SECRET` | GCP Secret Manager key name |
| `WEBHOOK_SECRET` | Webhook signature validation |
| `CONFIG_FILE` | Path to workflow config |
| `COPIER_COMMIT_STRATEGY` | Default: `direct` or `pr` |

## Common Tasks

### Add new transformation type
1. Add type constant in `types/config.go` (`TransformationType`)
2. Implement in `services/workflow_processor.go` (`processFileForWorkflow`)
3. Add tests in `services/workflow_processor_test.go`

### Add new pattern type
1. Add type in `types/config.go` (`PatternType`)
2. Implement in `services/pattern_matcher.go`
3. Add tests in `services/pattern_matcher_test.go`

### Modify webhook handling
1. Edit `services/webhook_handler_new.go`
2. Test with `test-payloads/example-pr-merged.json`

### Add new config field
1. Add to struct in `types/config.go`
2. Update validation if needed
3. Update consumers in `services/workflow_processor.go`

## Error Handling

- All public functions return `error` (no `log.Fatal`)
- Use `fmt.Errorf("context: %w", err)` for wrapping
- Check nil before dereferencing GitHub API responses

## Known Issues

See `RECOMMENDATIONS.md` for pending improvements:
- Global mutable state (race conditions)
- Missing CI/CD pipeline
- Rate limiting not implemented

