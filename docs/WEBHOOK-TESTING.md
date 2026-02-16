# Webhook Testing Guide

Test the github-copier app locally using the `test-webhook` CLI tool and/or [smee.io](https://smee.io) webhook forwarding.

## Prerequisites

- The app built locally: `go build -o github-copier .`
- A `.env.test` file configured (see `configs/.env.local.example`)
- (Optional) A GitHub personal access token for fetching real PR data

## Quick Start

### 1. Build the Test Tool

```bash
make test-webhook

# Or manually:
go build -o test-webhook ./cmd/test-webhook
```

### 2. Start the App in Dry-Run Mode

```bash
./github-copier -env .env.test -dry-run
```

The `-dry-run` flag skips actual writes to target repos and tolerates auth failures with a test-only PEM key.

### 3. Send a Test Webhook

```bash
# Send the example payload to the local server
./test-webhook -payload testdata/example-pr-merged.json

# Preview the payload without sending
./test-webhook -payload testdata/example-pr-merged.json -dry-run

# Send with no arguments to use a built-in example payload
./test-webhook
```

## Test Scenarios

### Local Dry-Run

Test configuration, pattern matching, and transformations without touching GitHub:

```bash
# Terminal 1: start the app
./github-copier -env .env.test -dry-run

# Terminal 2: send a test webhook
./test-webhook -payload testdata/example-pr-merged.json
```

Check Terminal 1 logs for:
- `PR event received` — webhook parsed successfully
- `found matching workflows` — config loaded and workflows matched
- `File matched transformation` — pattern matching + path transforms working
- `[DRY-RUN] Would upload files` — files would be written (skipped in dry-run)

### Test with a Real PR

Fetch actual PR metadata and file lists from GitHub:

```bash
export GITHUB_TOKEN=ghp_your_token_here

./test-webhook -pr 123 -owner myorg -repo myrepo
```

Or use the interactive helper script:

```bash
./scripts/test-with-pr.sh 123 myorg myrepo
```

### Test Against a Remote Environment

```bash
./test-webhook -pr 123 -owner myorg -repo myrepo \
  -url https://your-service-url.run.app/events \
  -secret your-webhook-secret
```

### Webhook Forwarding with smee.io

To receive real GitHub webhook deliveries locally:

1. Create a channel at [smee.io](https://smee.io)
2. Set the smee URL as your GitHub App's webhook URL
3. Run the smee client locally:
   ```bash
   npx smee-client -u https://smee.io/YOUR_CHANNEL -t http://localhost:8080/events
   ```
4. Merge a PR in a source repo — the webhook will be forwarded to your local app

This approach tests the full end-to-end flow including real GitHub auth and API calls (requires a valid PEM key, not the test-only key).

## Test Tool Reference

### CLI Flags

| Flag | Description | Default |
|---|---|---|
| `-pr` | PR number to fetch from GitHub | — |
| `-owner` | Repository owner (required with `-pr`) | — |
| `-repo` | Repository name (required with `-pr`) | — |
| `-url` | Webhook endpoint URL | `http://localhost:8080/events` |
| `-secret` | Webhook secret for HMAC signature | — |
| `-payload` | Path to a custom payload JSON file | — |
| `-dry-run` | Print payload without sending | `false` |

### Environment Variables

| Variable | Description |
|---|---|
| `GITHUB_TOKEN` | GitHub personal access token (for `-pr` mode) |
| `WEBHOOK_SECRET` | Default webhook secret |
| `REPO_OWNER` | Default repository owner |
| `REPO_NAME` | Default repository name |
| `WEBHOOK_URL` | Default webhook URL |

## Validating Results

### App Logs (stdout)

The app emits structured JSON logs. Key messages to look for:

| Log Message | Meaning |
|---|---|
| `webhook received` | Payload arrived |
| `signature verified` | HMAC check passed |
| `PR event received` | Event parsed; check `action` and `merged` fields |
| `processing merged PR` | Accepted for processing |
| `found matching workflows` | Workflows matched the source repo/branch |
| `File matched transformation` | A file matched a pattern and was transformed |
| `[DRY-RUN] Would upload files` | Dry-run: files would be written |
| `--Done--` | Processing complete |

### Diagnostic Endpoints

```bash
# Liveness probe
curl http://localhost:8080/health | jq

# Readiness probe (checks GitHub auth)
curl http://localhost:8080/ready | jq

# Resolved config with secrets redacted
curl http://localhost:8080/config | jq

# Metrics (if enabled)
curl http://localhost:8080/metrics | jq
```

## Troubleshooting

### Webhook Returns 401 Unauthorized

The webhook secret doesn't match. Ensure the `-secret` flag (or `WEBHOOK_SECRET` env var) matches the app's `WEBHOOK_SECRET` config value. If testing locally without signature verification, leave `WEBHOOK_SECRET` empty in `.env.test`.

### Duplicate Delivery Skipped

The app deduplicates webhooks by the `X-GitHub-Delivery` header. If you redeliver the same webhook, restart the app to clear the in-memory tracker (TTL is 1 hour).

### Config Cache Returns Stale Data

Workflow configs are cached for 5 minutes (configurable via `CONFIG_CACHE_TTL_SECONDS`). Restart the app to force a fresh load after changing a remote workflow config.

### Files Not Matched

Use the `config-validator` CLI to test patterns:

```bash
go build -o config-validator ./cmd/config-validator

# Test a glob pattern
./config-validator test-pattern -type glob -pattern 'examples/**/*.go' -file 'examples/go/main.go'

# Test a regex pattern
./config-validator test-pattern -type regex -pattern '^examples/(?P<lang>[^/]+)/.*$' -file 'examples/go/main.go'
```

### Path Transformation Wrong

```bash
./config-validator test-transform \
  -source 'examples/go/main.go' \
  -template 'code/${filename}'
```

### Auth Fails in Dry-Run Mode

Expected when using a test-only PEM key. The app will log a warning and continue:

```
⚠️  GitHub auth skipped (dry-run): ...
```

The webhook handler will also fail auth and classify it as a permanent error (no retries). This confirms the routing logic works — use smee.io with a real PEM for full end-to-end testing.

### No Matching Workflows

Check that:
1. The workflow config in the source repo uses the `workflows:` schema (not `workflow_configs:`)
2. The `source.repo` and `source.branch` in the workflow match the webhook's repository and base branch
3. The workflow config reference is `enabled: true` in the main config

## Makefile Targets

```bash
make test-webhook              # Build the test-webhook tool
make test-webhook-example      # Build + send example payload
make test-webhook-pr PR=123 OWNER=org REPO=repo  # Build + send real PR data
```
