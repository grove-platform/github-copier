# Helper Scripts

Collection of helper scripts for developing, testing, and deploying the github-copier application.

## Scripts

### ci-local.sh

Run the full CI pipeline locally before pushing. Mirrors what `.github/workflows/ci.yml` does.

**Usage:**
```bash
./scripts/ci-local.sh
```

**What it does:**
1. `go build ./...`
2. `go test -race ./...`
3. `golangci-lint run ./...`
4. `go vet ./...`

### run-local.sh

Start the github-copier application locally with development settings.

**Usage:**
```bash
./scripts/run-local.sh
```

**What it does:**
- Builds the `github-copier` binary if needed
- Disables Google Cloud logging (uses stdout)
- Enables dry-run mode, debug logging, and metrics
- Loads env from `configs/.env` if present
- Starts the application on port 8080

### deploy-cloudrun.sh

Deploy to Google Cloud Run.

**Usage:**
```bash
./scripts/deploy-cloudrun.sh [region]
```

**What it does:**
- Validates `env-cloudrun.yaml` exists
- Confirms deployment with user
- Deploys via `gcloud run deploy` with Dockerfile
- Prints service URL and next steps

### grant-secret-access.sh

Grant the Cloud Run service account access to all required secrets in Secret Manager.

**Usage:**
```bash
./scripts/grant-secret-access.sh
```

**Secrets configured:** `CODE_COPIER_PEM`, `webhook-secret`, `mongo-uri`

### test-and-check.sh

Send a test webhook and check health/metrics.

**Usage:**
```bash
./scripts/test-and-check.sh
```

**What it does:**
1. Sends test webhook with example payload
2. Waits for processing
3. Fetches and displays `/metrics` and `/health`

### test-with-pr.sh

Fetch real PR data from GitHub and send it to the webhook.

**Usage:**
```bash
./scripts/test-with-pr.sh <pr-number> [owner] [repo]
```

**Environment:**
- `GITHUB_TOKEN` — GitHub personal access token (required)
- `WEBHOOK_URL` — Webhook endpoint (default: `http://localhost:8080/events`)
- `WEBHOOK_SECRET` — Webhook secret for HMAC signature

### integration-test.sh

End-to-end integration test: sends a webhook payload and optionally verifies destination repos.

**Usage:**
```bash
./scripts/integration-test.sh webhook   # Send test webhook
./scripts/integration-test.sh verify    # Check dest repos
./scripts/integration-test.sh full      # Both + wait
```

**Environment:**
- `APP_URL` — App URL (default: `http://localhost:8080`)
- `WEBHOOK_SECRET` — Webhook secret (default: reads from `.env.test`)
- `DEST_REPO_1`, `DEST_PATH_1` — First destination repo/path to verify
- `DEST_REPO_2`, `DEST_PATH_2` — Second destination repo/path to verify

### test-slack.sh

Test Slack notifications by sending example messages (success, error, deprecation).

**Usage:**
```bash
./scripts/test-slack.sh [webhook-url]
# Or: export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/..."
```

### diagnose-github-auth.sh

Diagnostic script for GitHub App authentication issues. Checks Secret Manager, private key format, env config, and Cloud Run service health/readiness.

**Usage:**
```bash
./scripts/diagnose-github-auth.sh
```

### test-github-access.sh

Test if the deployed Cloud Run service can access the configured GitHub repository. Checks the `/ready` endpoint and recent logs for 401 errors.

**Usage:**
```bash
./scripts/test-github-access.sh
```

### check-installation-repos.sh

List all repositories accessible to a GitHub App installation. Generates a JWT, exchanges it for an installation token, and queries the GitHub API.

**Usage:**
```bash
./scripts/check-installation-repos.sh
```

**Requires:** `jq`, `ruby` with `jwt` gem, `gcloud`

## Common Workflows

### Local Development

```bash
# 1. Start app locally (dry-run + debug)
./scripts/run-local.sh

# 2. In another terminal, test it
./scripts/test-and-check.sh

# 3. Check metrics
curl http://localhost:8080/metrics | jq
```

### Pre-Push Validation

```bash
./scripts/ci-local.sh
```

### Testing with Real Data

```bash
# 1. Start app
./scripts/run-local.sh

# 2. Set GitHub token
export GITHUB_TOKEN=ghp_...

# 3. Test with real PR
./scripts/test-with-pr.sh 42 myorg myrepo
```

### Testing Slack Integration

```bash
export SLACK_WEBHOOK_URL="https://hooks.slack.com/services/..."
./scripts/test-slack.sh
```

### Diagnosing Auth Issues

```bash
# Full diagnostic
./scripts/diagnose-github-auth.sh

# Check repo access
./scripts/test-github-access.sh

# List accessible repos
./scripts/check-installation-repos.sh
```

## See Also

- [Local Testing Guide](../docs/LOCAL-TESTING.md) - Local development
- [Webhook Testing Guide](../docs/WEBHOOK-TESTING.md) - Testing webhooks
- [test-webhook Tool](../cmd/test-webhook/README.md) - Test webhook tool
