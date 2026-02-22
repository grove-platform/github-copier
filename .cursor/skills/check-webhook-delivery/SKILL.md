---
name: check-webhook-delivery
description: Check if a webhook was delivered and processed successfully. Use when the user merged a PR and wants to verify the copier handled it correctly.
---

# Check Webhook Delivery

Quickly verify whether a merged PR triggered successful file copying.

## Quick Checks

### 1. Check Running App Logs

If the app is running locally, check the terminal output for the delivery:

```bash
# In the terminal running github-copier, look for:
# - "received webhook" with the delivery ID
# - "processing PR merge" with repo/PR number
# - "successfully created commit" or "successfully created PR"
# - Any "error" or "failed" messages
```

**Success indicators:**
```
INFO received webhook delivery_id=<guid> event=pull_request action=closed
INFO processing PR merge repo=org/source-repo pr=123 merged=true
INFO matched N files for workflow workflow=sync-docs
INFO successfully created PR target_repo=org/dest-repo pr_url=https://github.com/...
```

**Failure indicators:**
```
ERROR failed to process webhook error="..." delivery_id=<guid>
WARN no matching files for any workflow
ERROR rate limited, will retry
ERROR permanent error, not retrying error="config validation failed"
```

### 2. Check Cloud Run Logs (Production)

```bash
# Recent logs (last 10 minutes)
gcloud logging read "resource.type=cloud_run_revision AND resource.labels.service_name=github-copier" \
  --limit=50 \
  --format="table(timestamp,jsonPayload.msg,jsonPayload.delivery_id,jsonPayload.error)"

# Filter by delivery ID (if known)
gcloud logging read "resource.type=cloud_run_revision AND jsonPayload.delivery_id=\"<DELIVERY-ID>\"" \
  --limit=20

# Filter by source repo
gcloud logging read "resource.type=cloud_run_revision AND jsonPayload.repo=\"org/source-repo\"" \
  --limit=20 \
  --freshness=1h

# Show only errors
gcloud logging read "resource.type=cloud_run_revision AND resource.labels.service_name=github-copier AND severity>=ERROR" \
  --limit=20
```

### 3. Check GitHub for Delivery Status

Go to **GitHub → Source Repo → Settings → Webhooks → Recent Deliveries**

Look for:
- ✅ Green check = webhook delivered successfully (2xx response)
- ❌ Red X = delivery failed (4xx/5xx response)
- 🔄 Pending = still processing or timeout

Click on a delivery to see:
- Request payload (PR number, files changed)
- Response body (any error messages from the app)
- `X-GitHub-Delivery` header (the delivery ID for log correlation)

### 4. Check Target Repo for Results

After successful processing, check the target repo:

```bash
# Check for new PRs from the copier
gh pr list --repo org/dest-repo --author "app/github-copier" --state open

# Check recent commits (for direct commit strategy)
gh api repos/org/dest-repo/commits --jq '.[0:5] | .[] | "\(.sha[0:7]) \(.commit.message | split("\n")[0])"'

# Check for copier branches
gh api repos/org/dest-repo/branches --jq '.[] | select(.name | startswith("copier/")) | .name'
```

## Common Issues

| Symptom | Likely Cause | Check |
|---------|--------------|-------|
| No logs at all | Webhook not delivered | GitHub webhook settings, URL correct? |
| "signature verification failed" | Wrong webhook secret | Compare secrets in GCP and GitHub |
| "no matching files" | Pattern doesn't match | Test patterns with `config-validator` |
| "rate limited" | GitHub API quota | Wait for reset, check `/metrics` |
| "config load failed" | Bad YAML in config repo | Validate config file |
| "installation not found" | App not installed on repo | Check GitHub App installations |

## Correlating Logs

Every webhook has a unique `X-GitHub-Delivery` ID (a GUID). Use this to trace a single request through all log entries:

1. Find the delivery ID in GitHub webhook history
2. Search logs: `delivery_id=<GUID>`
3. All log lines for that request will have the same ID

## Health Endpoints

```bash
# Basic liveness (is app running?)
curl https://your-app-url/health

# Deep readiness (GitHub auth working? Rate limits OK?)
curl https://your-app-url/ready

# Metrics (request counts, error rates)
curl https://your-app-url/metrics
```
