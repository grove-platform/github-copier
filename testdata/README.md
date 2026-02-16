# Test Data

Test fixtures for local and CI testing of the github-copier application.

## Directory Structure

```
testdata/
├── README.md                     # This file
├── .env.test                     # Test environment template
├── test-config.yaml              # Example workflow config (inline format)
├── source-repo-files/            # Files to copy to a test source repo
│   └── .copier/
│       └── main.yaml             # Main config format example
│
├── # Webhook Payloads - Merged PRs (should trigger processing)
├── example-pr-merged.json        # Generic merged PR example
├── test-pr-merged.json           # Test repo merged PR
├── pr-multiple-workflows.json    # Files matching multiple workflows
├── pr-large-changeset.json       # Large PR (35+ files) for pagination testing
├── pr-renamed-files.json         # Files with status: renamed
├── pr-with-deprecations.json     # Mix of added and removed files
│
├── # Webhook Payloads - Non-merged (should be ignored)
├── pr-closed-not-merged.json     # Closed without merge
├── pr-opened.json                # PR opened event
├── pr-synchronize.json           # PR updated with new commits
├── pr-no-matching-files.json     # Merged but no files match patterns
│
└── push-to-main.json             # Direct push event (if supported)
```

## Quick Start

### Option 1: Automated Integration Tests

```bash
# Run full integration test suite
./scripts/integration-test.sh

# Quick smoke test only
./scripts/integration-test.sh --quick

# Verbose output
./scripts/integration-test.sh --verbose
```

### Option 2: Manual Testing

```bash
# 1. Start the app in dry-run mode
make run-local-quick

# 2. In another terminal, send a test webhook
make test-webhook-example

# Or use the test-webhook CLI directly
./test-webhook -payload testdata/example-pr-merged.json
```

### Option 3: Test with Real GitHub Data

```bash
# Set your GitHub token (see "GitHub Token Requirements" below)
export GITHUB_TOKEN=ghp_your_token_here

# Fetch and send real PR data
./test-webhook -pr 123 -owner myorg -repo myrepo
```

#### GitHub Token Requirements

The token is used to fetch PR metadata and file lists from the GitHub API.

**Classic Personal Access Token:**
- Go to https://github.com/settings/tokens
- Generate new token (classic)
- Required scope: `repo` (or `public_repo` for public repos only)

**Fine-grained Personal Access Token:**
- Go to https://github.com/settings/tokens?type=beta
- Select the specific repositories you need
- Required permissions:
  - Pull requests: **Read-only**
  - Contents: **Read-only**

## Test Payloads

> **Important:** Test payloads contain fake PR numbers. The app will match workflows
> but fail when fetching files from GitHub (the PR doesn't exist). To test the full
> pipeline, use a **real PR number** with `./test-webhook -pr <NUMBER> -owner <OWNER> -repo <REPO>`.

### Merged PRs (Trigger Processing)

| File | Description | Expected Behavior |
|------|-------------|-------------------|
| `example-pr-merged.json` | Generic example with Go/Python files | Matches workflows, fails on file fetch (fake PR) |
| `test-pr-merged.json` | Test repo specific | Matches workflows, fails on file fetch (fake PR) |
| `pr-multiple-workflows.json` | Files for Go, Python, JS, and docs | Should trigger multiple workflows |
| `pr-large-changeset.json` | 35 files across multiple categories | Tests batch processing |
| `pr-renamed-files.json` | Files with `status: renamed` | Tests rename handling |
| `pr-with-deprecations.json` | Mix of added and removed files | Tests deprecation tracking |

### Non-Merged Events (Should Be Ignored)

| File | Description | Expected Behavior |
|------|-------------|-------------------|
| `pr-closed-not-merged.json` | Closed without merge | Should be acknowledged but not processed |
| `pr-opened.json` | PR opened event | Should be ignored |
| `pr-synchronize.json` | PR updated | Should be ignored |
| `pr-no-matching-files.json` | Merged but only CI/config files | No files match patterns |

## What You Can Test

### With Fake Payloads (testdata/*.json)

These test payloads use fake PR numbers, so they validate:
- ✅ Webhook signature verification
- ✅ Payload parsing
- ✅ Config loading and caching
- ✅ Workflow matching (source repo + branch)
- ✅ Slack error notifications
- ❌ File fetching (fails - PR doesn't exist)
- ❌ File processing and copying

### With Real PRs (-pr flag)

Using a real PR number tests the complete pipeline:
```bash
export GITHUB_TOKEN=ghp_...
./test-webhook -pr <REAL_PR_NUMBER> -owner cbullinger -repo copier-app-source-test -secret test-secret
```

This validates:
- ✅ Everything above, plus:
- ✅ File fetching from GitHub
- ✅ Pattern matching on actual files
- ✅ File content retrieval
- ✅ Path transformations
- ✅ Commit/PR creation (unless DRY_RUN=true)
- ✅ Slack success notifications

### Other Events

| File | Description | Expected Behavior |
|------|-------------|-------------------|
| `push-to-main.json` | Direct push to main branch | Depends on app configuration |

## Configuration Files

### test-config.yaml

Example workflow configuration using the **inline workflows format**:

```yaml
workflows:
  - name: "workflow-name"
    source:
      repo: "owner/repo"
      branch: "main"
      patterns:
        - "examples/go/**"
    destination:
      repo: "owner/target-repo"
      branch: "main"
    transformations:
      - move:
          from: "examples/go/"
          to: "go-examples/"
    commit_strategy:
      type: "pull_request"
      pr_title: "Sync examples"
```

### source-repo-files/.copier/main.yaml

Example main config using the **workflow_configs format** (for source repos):

```yaml
defaults:
  commit_strategy:
    type: "pull_request"
    auto_merge: false

workflow_configs:
  - source: "inline"
    workflows:
      - name: "workflow-name"
        # ... workflow definition
```

## Environment Configuration

Copy `.env.test` to the project root and configure:

```bash
cp testdata/.env.test .env.test
# Edit .env.test with your values
```

Key settings for testing:

| Variable | Description | Test Value |
|----------|-------------|------------|
| `DRY_RUN` | Skip actual writes | `true` for safe testing |
| `COPIER_DISABLE_CLOUD_LOGGING` | Use stdout logging | `true` |
| `LOG_LEVEL` | Logging verbosity | `debug` for troubleshooting |
| `AUDIT_ENABLED` | MongoDB audit logging | `false` (no MongoDB needed) |

## Creating Custom Payloads

1. Copy an existing payload as a template
2. Modify the `files` array to test specific patterns
3. Update PR metadata as needed

Example for testing a specific pattern:

```json
{
  "action": "closed",
  "pull_request": {
    "merged": true,
    "merge_commit_sha": "abc123"
  },
  "repository": {
    "full_name": "cbullinger/copier-app-source-test"
  },
  "files": [
    {
      "filename": "examples/go/your-test-file.go",
      "status": "added"
    }
  ]
}
```

## Validating Results

### Check Application Logs

```bash
# Logs go to stdout in local mode
# Look for:
# - "webhook received" - payload arrived
# - "PR merged" - event recognized
# - "found matching workflows" - config loaded
# - "File matched transformation" - pattern matched
# - "[DRY-RUN] Would upload" - files would be written
```

### Check Metrics

```bash
curl http://localhost:8080/metrics | jq
```

### Check Health

```bash
curl http://localhost:8080/health | jq
```

## Test Repositories

For isolated end-to-end testing, you can set up dedicated test repositories:

| Repo | Purpose |
|------|---------|
| `your-org/copier-source-test` | Source repo that triggers webhooks |
| `your-org/copier-dest-test-1` | First destination repo |
| `your-org/copier-dest-test-2` | Second destination repo |

Configure these in your test workflow config and `.env.test`.

## Troubleshooting

### Webhook returns 401

Signature verification failed. Either:
- Set `WEBHOOK_SECRET` to match your test secret
- Clear `WEBHOOK_SECRET` to skip verification in testing

### No files matched

Use `config-validator` to test patterns:

```bash
./config-validator test-pattern \
  -type glob \
  -pattern 'examples/go/**' \
  -file 'examples/go/main.go'
```

### App won't start

Check for port conflicts:

```bash
lsof -i :8080
```

Check logs:

```bash
cat /tmp/integration-test-app.log
```

## See Also

- [LOCAL-TESTING.md](../docs/LOCAL-TESTING.md) - Full local testing guide
- [WEBHOOK-TESTING.md](../docs/WEBHOOK-TESTING.md) - Webhook-specific testing
- [CONFIG-REFERENCE.md](../docs/CONFIG-REFERENCE.md) - Configuration schema
