# Configuration Reference

Complete reference for all github-copier configuration options: environment variables (app config) and YAML schema (workflow config).

## Table of Contents

- [Environment Variables](#environment-variables)
  - [Required](#required)
  - [Config Repository](#config-repository)
  - [GitHub App Authentication](#github-app-authentication)
  - [Webhook](#webhook)
  - [Commit Defaults](#commit-defaults)
  - [Dry Run / Debug](#dry-run--debug)
  - [Slack Notifications](#slack-notifications)
  - [Audit Logging](#audit-logging)
  - [GitHub API Tuning](#github-api-tuning)
  - [Webhook Processing](#webhook-processing)
  - [Google Cloud](#google-cloud)
- [Workflow YAML Schema](#workflow-yaml-schema)
  - [Main Config](#main-config)
  - [Workflow Config](#workflow-config)
  - [Workflow](#workflow)
  - [Source / Destination](#source--destination)
  - [Transformations](#transformations)
  - [Commit Strategy](#commit-strategy)
  - [Defaults](#defaults)
  - [$ref Support](#ref-support)

---

## Environment Variables

Set via `.env` files, `env-cloudrun.yaml`, or process environment.

### Required

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `CONFIG_REPO_NAME` | string | — | Repository containing the workflow config files. |
| `CONFIG_REPO_OWNER` | string | — | Owner (org or user) of the config repository. |
| `GITHUB_APP_ID` | string | — | GitHub App ID for authentication. |
| `INSTALLATION_ID` | string | — | GitHub App installation ID. |

### Config Repository

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `CONFIG_REPO_BRANCH` | string | `main` | Branch to fetch config files from. |
| `CONFIG_FILE` | string | `copier-config.yaml` | Legacy single-file config path. Ignored when `USE_MAIN_CONFIG=true`. |
| `MAIN_CONFIG_FILE` | string | — | Path to the main config file (e.g. `.copier/main.yaml`). Enables multi-file config. |
| `USE_MAIN_CONFIG` | bool | `true` if `MAIN_CONFIG_FILE` is set | Whether to use the main config format. |
| `DEPRECATION_FILE` | string | `deprecated_examples.json` | Path to the deprecation tracking file. |
| `CONFIG_CACHE_TTL_SECONDS` | int | `300` | How long (seconds) to cache resolved workflow configs. `0` disables caching. |

### GitHub App Authentication

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `GITHUB_APP_CLIENT_ID` | string | — | GitHub App client ID (optional). |
| `PEM_NAME` | string | `CODE_COPIER_PEM` | GCP Secret Manager secret name for the PEM key. Resolved to full path via `SecretPath()`. |
| `SKIP_SECRET_MANAGER` | bool | `false` | If `true`, skip GCP Secret Manager and use `GITHUB_APP_PRIVATE_KEY_B64` instead. |
| `GITHUB_APP_PRIVATE_KEY_B64` | string | — | Base64-encoded PEM private key. Used when `SKIP_SECRET_MANAGER=true`. |

### Webhook

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `PORT` | string | `8080` | HTTP server listen port. |
| `WEBSERVER_PATH` | string | `/events` | Path for the webhook endpoint. |
| `WEBHOOK_SECRET` | string | — | Webhook HMAC secret for signature verification. Leave empty to disable (not recommended in production). |
| `WEBHOOK_SECRET_NAME` | string | `webhook-secret` | GCP Secret Manager name for the webhook secret. |

### Commit Defaults

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `COMMITTER_NAME` | string | `Copier Bot` | Git committer name for generated commits. |
| `COMMITTER_EMAIL` | string | `bot@example.com` | Git committer email. |
| `DEFAULT_COMMIT_MESSAGE` | string | `Automated PR with updated examples` | Fallback commit message when per-workflow message is absent. |
| `DEFAULT_RECURSIVE_COPY` | bool | `true` | System-wide default for recursive copying (individual configs can override). |
| `DEFAULT_PR_MERGE` | bool | `false` | System-wide default for PR auto-merge without review. |

### Dry Run / Debug

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `DRY_RUN` | bool | `false` | Process webhooks but don't make actual commits/PRs. |
| `LOG_LEVEL` | string | — | Log level (`debug`, `info`, `warn`, `error`). |
| `COPIER_DEBUG` | bool | `false` | Enable extra debug info in logs. |
| `COPIER_DISABLE_CLOUD_LOGGING` | bool | `false` | Use stdout instead of GCP Cloud Logging. |
| `METRICS_ENABLED` | bool | `true` | Enable the `/metrics` endpoint. |

### Slack Notifications

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `SLACK_WEBHOOK_URL` | string | — | Slack incoming webhook URL. Leave empty to disable notifications. |
| `SLACK_CHANNEL` | string | `#code-examples` | Slack channel for notifications. |
| `SLACK_USERNAME` | string | `Examples Copier` | Bot username shown in Slack. |
| `SLACK_ICON_EMOJI` | string | `:robot_face:` | Bot icon emoji. |
| `SLACK_ENABLED` | bool | `true` if URL is set | Explicitly enable/disable Slack. |

### Audit Logging

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `AUDIT_ENABLED` | bool | `false` | Enable MongoDB audit logging. |
| `MONGO_URI` | string | — | MongoDB connection URI. |
| `MONGO_URI_SECRET_NAME` | string | — | GCP Secret Manager name for the MongoDB URI. |
| `AUDIT_DATABASE` | string | `copier_audit` | MongoDB database name. |
| `AUDIT_COLLECTION` | string | `events` | MongoDB collection name. |

### GitHub API Tuning

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `GITHUB_API_MAX_RETRIES` | int | `3` | Number of retry attempts for GitHub API calls (rate limits, transient errors). |
| `GITHUB_API_INITIAL_RETRY_DELAY` | int | `500` | Initial retry delay in **milliseconds** (doubles each attempt). |
| `PR_MERGE_POLL_MAX_ATTEMPTS` | int | `20` | Max attempts to poll PR mergeability after creation. |
| `PR_MERGE_POLL_INTERVAL` | int | `500` | Polling interval in **milliseconds**. |

### Webhook Processing

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `WEBHOOK_PROCESSING_TIMEOUT_SECONDS` | int | `300` | Timeout for the background webhook processing goroutine. `0` disables the timeout. |
| `WEBHOOK_MAX_RETRIES` | int | `2` | Max retry attempts for failed webhook processing (total attempts = retries + 1). |
| `WEBHOOK_RETRY_INITIAL_DELAY` | int | `5` | Initial delay between retries in **seconds** (doubles each attempt). |

### Google Cloud

| Variable | Type | Default | Description |
|----------|------|---------|-------------|
| `GOOGLE_CLOUD_PROJECT_ID` | string | `github-copy-code-examples` | GCP project ID for Cloud Logging and Secret Manager. |
| `COPIER_LOG_NAME` | string | `copy-copier-log` | Log name in GCP Cloud Logging. |

---

## Workflow YAML Schema

### Main Config

The main config file (e.g. `.copier/main.yaml`) references one or more workflow config files.

```yaml
# Optional global defaults applied to all workflows
defaults:
  commit_strategy:
    type: pull_request
    commit_message: "chore: sync from source"
  exclude:
    - "*.test.go"

# List of workflow config sources
workflow_configs:
  # Load from a file in the same repo
  - source: local
    path: .copier/workflows/docs.yaml

  # Load from a different repo
  - source: repo
    repo: org/other-repo
    path: copier-config.yaml
    branch: main  # default: main

  # Inline workflows directly
  - source: inline
    workflows:
      - name: inline-example
        source:
          repo: org/source
        destination:
          repo: org/target
        transformations:
          - copy:
              from: README.md
              to: README.md

  # Disable a config without removing it
  - source: local
    path: .copier/workflows/experimental.yaml
    enabled: false
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `defaults` | [Defaults](#defaults) | No | Global defaults inherited by all workflows. |
| `workflow_configs` | array | Yes | List of workflow config references. |
| `workflow_configs[].source` | string | Yes | `local`, `repo`, or `inline`. |
| `workflow_configs[].path` | string | Yes (local/repo) | Path to the config file. |
| `workflow_configs[].repo` | string | Yes (repo) | `owner/name` of the repository. |
| `workflow_configs[].branch` | string | No | Branch to fetch from. Default: `main`. |
| `workflow_configs[].enabled` | bool | No | Set `false` to skip this config. Default: `true`. |
| `workflow_configs[].workflows` | array | Yes (inline) | Inline workflow definitions. |

### Workflow Config

A workflow config file (referenced by the main config) contains one or more workflows and optional local defaults.

```yaml
defaults:
  commit_strategy:
    type: direct

workflows:
  - name: sync-examples
    source:
      repo: org/source-repo
      branch: main
    destination:
      repo: org/target-repo
      branch: main
    transformations:
      - copy:
          from: examples/hello.go
          to: examples/hello.go
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `defaults` | [Defaults](#defaults) | No | Local defaults for workflows in this file. |
| `workflows` | array | Yes | One or more workflow definitions. |

### Workflow

```yaml
- name: my-workflow                # Required: unique name
  source:
    repo: org/source-repo          # Required: owner/name
    branch: main                   # Optional, default: main
    installation_id: "12345"       # Optional: override default installation
  destination:
    repo: org/target-repo          # Required: owner/name
    branch: main                   # Optional, default: main
    installation_id: "67890"       # Optional: override
  transformations:                 # Required: at least one
    - copy:
        from: docs/guide.md
        to: docs/guide.md
  exclude:                         # Optional: regex patterns
    - ".*_test\\.go$"
  commit_strategy:                 # Optional (inherits from defaults)
    type: pull_request
    pr_title: "Copier: sync docs"
  deprecation_check:               # Optional
    enabled: true
    file: deprecated_examples.json
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique workflow name (used in logs and metrics). |
| `source` | [Source](#source--destination) | Yes | Where to read files from. |
| `destination` | [Source](#source--destination) | Yes | Where to write files to. |
| `transformations` | array | Yes | List of file transformations. At least one required. |
| `exclude` | string[] | No | Regex patterns for files to skip. |
| `commit_strategy` | [CommitStrategy](#commit-strategy) | No | How to deliver changes. Inherits from defaults. |
| `deprecation_check` | object | No | Track file deletions. |

### Source / Destination

```yaml
source:
  repo: org/repo-name        # Required
  branch: main               # Optional, default: main
  installation_id: "12345"   # Optional: GitHub App installation override
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `repo` | string | Yes | — | `owner/name` format. |
| `branch` | string | No | `main` | Branch to read from / write to. |
| `installation_id` | string | No | — | Override the default GitHub App installation ID. |

### Transformations

Each transformation matches source files and maps them to destination paths. Exactly one type per entry.

#### `copy` — Single file copy

```yaml
- copy:
    from: path/to/source.go    # Exact source path
    to: path/to/dest.go        # Exact destination path
```

#### `move` — Directory move (recursive)

```yaml
- move:
    from: examples/go/         # Source directory prefix
    to: sdk/go/                # Destination directory prefix
```

Files under `from` are placed under `to` preserving relative structure.

#### `glob` — Glob pattern with template

```yaml
- glob:
    pattern: "examples/**/*.go"                # Glob pattern
    transform: "sdk/${relative_path}"          # Template with variables
```

**Built-in variables:** `${path}`, `${filename}`, `${dir}`, `${ext}`, `${relative_path}`.

#### `regex` — Regex with named capture groups

```yaml
- regex:
    pattern: "^examples/(?P<lang>[^/]+)/(?P<file>.+)$"
    transform: "sdk/${lang}/${file}"
```

Named groups (`?P<name>`) become template variables. Built-in variables are also available.

### Commit Strategy

Controls how changes are delivered to the destination repository.

```yaml
commit_strategy:
  type: pull_request           # "direct" or "pull_request"
  commit_message: "chore: sync from source"
  pr_title: "Copier: update examples"
  pr_body: "Automated sync from source repo."
  use_pr_template: false       # Fetch PR template from target repo
  auto_merge: false            # Auto-merge the PR (requires branch protection)
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `type` | string | `pull_request` | `direct` (commit to branch) or `pull_request` (open PR). |
| `commit_message` | string | env `DEFAULT_COMMIT_MESSAGE` | Git commit message. |
| `pr_title` | string | same as `commit_message` | PR title (only for `pull_request` type). |
| `pr_body` | string | — | PR body text. |
| `use_pr_template` | bool | `false` | If true, fetch `PULL_REQUEST_TEMPLATE.md` from target repo. |
| `auto_merge` | bool | `false` | If true, enable auto-merge on the created PR. |

**Important:** If multiple workflows target the same `(repo, branch)` with the same strategy, their files are **batched into a single commit/PR**. The last workflow's metadata (title, body, message) wins. A warning is logged at config load time if workflows use different strategies for the same target.

### Defaults

Defaults can be set at three levels (highest to lowest priority):
1. **Workflow-level** — set directly on the workflow.
2. **Config-file-level** — set in the workflow config file's `defaults` block.
3. **Global-level** — set in the main config file's `defaults` block.

```yaml
defaults:
  commit_strategy:
    type: direct
    commit_message: "chore: automated sync"
  deprecation_check:
    enabled: true
    file: deprecated_examples.json
  exclude:
    - ".*_test\\.go$"
    - "\\.github/.*"
```

| Field | Type | Description |
|-------|------|-------------|
| `commit_strategy` | [CommitStrategy](#commit-strategy) | Default commit strategy for workflows that don't specify one. |
| `deprecation_check` | object | Default deprecation settings. |
| `exclude` | string[] | Default exclude patterns (regex). |

### $ref Support

Workflow fields (`transformations`, `commit_strategy`, `exclude`) support `$ref` to reference shared definitions in other files:

```yaml
workflows:
  - name: example
    source:
      repo: org/source
    destination:
      repo: org/target
    transformations:
      $ref: shared/transforms.yaml
    commit_strategy:
      $ref: shared/pr-strategy.yaml
    exclude:
      $ref: shared/excludes.yaml
```

The referenced file is loaded relative to the config repository root.
