# Kanopy Deployment Guide

This guide walks you through deploying github-copier to Kanopy for the first time.

## Overview

We're deploying github-copier to Kanopy to enable:
1. **Webhook ingress with IP allowlisting** - Restrict access to GitHub webhook IPs only
2. **Integration with Kanopy observability** - Prometheus metrics, Grafana dashboards
3. **Standard MongoDB deployment pattern** - Consistent with other internal tools

## Architecture

```
GitHub Webhooks
    ↓
Vanity Hostname (with IP allowlist)
github-webhooks.staging.corp.mongodb.com
    ↓
Kanopy Ingress Gateway
    ↓
github-copier Service (ClusterIP)
    ↓
github-copier Pods (1-3 replicas)
```

## Prerequisites

### 1. Kanopy Access

```bash
# Install kanopy-oidc
brew install kanopy-oidc

# Authenticate to staging
kanopy-oidc kube login
# Select: staging

# Set kubeconfig
export KUBECONFIG=~/.kube/config.staging

# Verify access to docs namespace
kubectl get pods -n docs
```

### 2. Artifactory Access

```bash
# Login to Artifactory
docker login artifactory.corp.mongodb.com
# Username: your.mongodb.email@mongodb.com
# Password: Your Artifactory API token (get from https://artifactory.corp.mongodb.com)
```

### 3. GitHub App Credentials

You'll need:
- **App ID** - From GitHub App settings
- **Installation ID** - From GitHub App installation
- **Private Key (PEM file)** - Downloaded from GitHub App settings
- **Webhook Secret** - Generate a new one for Kanopy deployment

## Step-by-Step Deployment

### Step 1: Build and Push Docker Image

```bash
# Navigate to project root
cd /Users/cbullinger/devdocs/grove/github-copier

# Build the image
docker build -t artifactory.corp.mongodb.com/docs-docker-local/github-copier:v0.2.0 .

# Push to Artifactory
docker push artifactory.corp.mongodb.com/docs-docker-local/github-copier:v0.2.0

# Tag as latest
docker tag artifactory.corp.mongodb.com/docs-docker-local/github-copier:v0.2.0 \
           artifactory.corp.mongodb.com/docs-docker-local/github-copier:latest
docker push artifactory.corp.mongodb.com/docs-docker-local/github-copier:latest
```

### Step 2: Create Kubernetes Secrets

**IMPORTANT:** Secret names are prefixed with `github-copier-` to avoid collisions in the `docs` namespace.

```bash
# Set kubeconfig
export KUBECONFIG=~/.kube/config.staging

# 1. Create app credentials secret
kubectl create secret generic github-copier-app-credentials \
  --from-literal=GITHUB_APP_ID="YOUR_APP_ID" \
  --from-literal=INSTALLATION_ID="YOUR_INSTALLATION_ID" \
  -n docs

# 2. Create PEM secret (replace with your actual PEM file path)
kubectl create secret generic github-copier-pem \
  --from-file=GITHUB_APP_PRIVATE_KEY=/path/to/your-app.private-key.pem \
  -n docs

# 3. Generate and create webhook secret
WEBHOOK_SECRET=$(openssl rand -hex 32)
echo "Save this webhook secret for GitHub App configuration: $WEBHOOK_SECRET"

kubectl create secret generic github-copier-webhook-secret \
  --from-literal=WEBHOOK_SECRET="$WEBHOOK_SECRET" \
  -n docs

# 4. Verify secrets were created
kubectl get secrets -n docs | grep github-copier
```

**Save the webhook secret!** You'll need it when configuring the GitHub App webhook URL.

### Step 3: Deploy with Helm

```bash
# Add MongoDB helm repo
helm repo add mongodb https://10gen.github.io/helm-charts
helm repo update

# Deploy to staging
cd /Users/cbullinger/devdocs/grove/github-copier
helm upgrade --install github-copier mongodb/web-app \
  --namespace docs \
  --values kanopy/staging/values.yaml \
  --set image.tag=v0.2.0 \
  --wait \
  --timeout 5m

# Check deployment status
kubectl get pods -n docs -l app=github-copier
kubectl rollout status deployment/github-copier -n docs
```

### Step 4: Verify Deployment

```bash
# Check pod status
kubectl get pods -n docs -l app=github-copier

# Check logs
kubectl logs -n docs -l app=github-copier --tail=100 -f

# Port-forward to test locally
kubectl port-forward -n docs svc/github-copier 8080:80

# In another terminal, test endpoints:
curl http://localhost:8080/health
# Expected: {"status":"healthy"}

curl http://localhost:8080/ready
# Expected: {"status":"ready"}

curl http://localhost:8080/metrics
# Expected: Prometheus metrics output
```

### Step 5: Test Webhook Endpoint

```bash
# Keep port-forward running from Step 4
# In another terminal:

cd /Users/cbullinger/devdocs/grove/github-copier

# Build test-webhook tool if not already built
go build -o test-webhook ./cmd/test-webhook

# Send test webhook
./test-webhook \
  -url http://localhost:8080/events \
  -secret "YOUR_WEBHOOK_SECRET" \
  -payload testdata/example-pr-merged.json

# Check logs for webhook processing
kubectl logs -n docs -l app=github-copier --tail=50
```

### Step 6: Submit KANOPY Ticket for Vanity Hostname

Now that the service is deployed and verified, submit a KANOPY ticket:

**URL:** https://jira.mongodb.org/plugins/servlet/desk/portal/48

**Form Details:**

- **Desired hostname:** `github-webhooks.staging.corp.mongodb.com`
- **Current endpoint:** `github-copier.docs.staging.corp.mongodb.com`
- **CorpSecure:** Disable (external GitHub webhooks)
- **Description:**
  ```
  Request for vanity hostname with IP allowlist for GitHub webhook receiver.
  
  Service: github-copier - Internal tool for documentation repository automation
  Intended Audience: External (GitHub webhook servers only)
  Data Sensitivity: Low (public repository events)
  
  Security:
  - IP Allowlist Required: GitHub webhook IP ranges only
  - GitHub webhook IPs: https://api.github.com/meta (hooks section)
  - Application validates HMAC-SHA256 signatures
  
  Why Disable CorpSecure:
  GitHub webhook servers cannot authenticate with employee credentials.
  Access control via IP allowlist + webhook signature validation.
  
  No cookies or trackers: Webhook receiver endpoint only.
  ```

### Step 7: Configure GitHub Webhook (After Vanity Hostname is Ready)

Once the Kanopy team sets up the vanity hostname:

1. Go to your GitHub App settings
2. Update webhook URL to: `https://github-webhooks.staging.corp.mongodb.com/events`
3. Set webhook secret to the value from Step 2
4. Save changes

### Step 8: Monitor and Test

```bash
# Watch logs for incoming webhooks
kubectl logs -n docs -l app=github-copier -f

# Create a test PR in a repository where the app is installed
# Watch the logs to see webhook processing

# Check metrics
kubectl port-forward -n docs svc/github-copier 8080:80
curl http://localhost:8080/metrics | grep github_copier
```

## Troubleshooting

See `kanopy/staging/README.md` for detailed troubleshooting steps.

## Next Steps

1. ✅ Deploy to staging (this guide)
2. ⏳ Wait for KANOPY ticket approval (vanity hostname)
3. ✅ Configure GitHub webhook with new URL
4. ✅ Test with real PRs
5. 📊 Set up Grafana dashboards (optional)
6. 🚀 Deploy to production (repeat process with `kanopy/production/`)

## References

- [Kanopy Staging README](staging/README.md)
- [Kanopy Ingress Options Analysis](staging/argo-events/KANOPY_INGRESS_OPTIONS.md)
- [GitHub Copier Documentation](../docs/)

