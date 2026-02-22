# Kanopy Staging Deployment

This directory contains the Kanopy deployment configuration for github-copier in the **staging** environment.

## Overview

- **Environment:** staging
- **Namespace:** docs
- **Cluster:** api.staging.corp.mongodb.com
- **Helm Chart:** mongodb/web-app
- **Default Hostname:** `github-copier.docs.staging.corp.mongodb.com`

## Prerequisites

1. **Kanopy Access**
   ```bash
   # Install kanopy-oidc if not already installed
   brew install kanopy-oidc
   
   # Authenticate to staging cluster
   kanopy-oidc kube login
   # Select: staging
   
   # Verify access
   export KUBECONFIG=~/.kube/config.staging
   kubectl get pods -n docs
   ```

2. **Docker Image**
   - Build and push to MongoDB's private ECR registry
   - Repository: `795250896452.dkr.ecr.us-east-1.amazonaws.com/docs/github-copier`
   - See [Building the Docker Image](#building-the-docker-image) below

3. **Secrets**
   - GitHub App credentials (App ID, Installation ID)
   - GitHub App private key (PEM file)
   - Webhook secret
   - See [Creating Secrets](#creating-secrets) below

## Files

- **`values.yaml`** - Helm chart values for web-app deployment
- **`secrets.yaml.template`** - Template for Kubernetes secrets (DO NOT commit with real values!)
- **`README.md`** - This file
- **`argo-events/`** - Argo Events testing artifacts (historical reference)

## Deployment Steps

### 1. Build and Push Docker Image

```bash
# From project root
cd /Users/cbullinger/devdocs/grove/github-copier

# Build the image
docker build -t 795250896452.dkr.ecr.us-east-1.amazonaws.com/docs/github-copier:v0.2.0 .

# Push the image (requires AWS ECR credentials - typically done via Drone CI/CD)
# For manual push, you need ecr_access_key and ecr_secret_key
# Usually this is automated via Drone, so you can skip the push for local testing

# Tag as latest
docker tag 795250896452.dkr.ecr.us-east-1.amazonaws.com/docs/github-copier:v0.2.0 \
           795250896452.dkr.ecr.us-east-1.amazonaws.com/docs/github-copier:latest
```

**Note:** For local testing, you don't need to push to ECR. Drone CI/CD will handle building and pushing automatically when you push to the `main` or `kanopy-deployment` branch.

### 2. Create Secrets

**IMPORTANT:** Secret names are prefixed with `github-copier-` to avoid collisions in the `docs` namespace.

```bash
# Set kubeconfig
export KUBECONFIG=~/.kube/config.staging

# Create app credentials secret
kubectl create secret generic github-copier-app-credentials \
  --from-literal=GITHUB_APP_ID="YOUR_APP_ID" \
  --from-literal=INSTALLATION_ID="YOUR_INSTALLATION_ID" \
  -n docs

# Create PEM secret (from file)
kubectl create secret generic github-copier-pem \
  --from-file=GITHUB_APP_PRIVATE_KEY=/path/to/your-app.private-key.pem \
  -n docs

# Create webhook secret
kubectl create secret generic github-copier-webhook-secret \
  --from-literal=WEBHOOK_SECRET="$(openssl rand -hex 32)" \
  -n docs

# Verify secrets were created
kubectl get secrets -n docs | grep github-copier
```

**Alternative: Use the template**

```bash
# Copy template
cp secrets.yaml.template secrets.yaml

# Edit with your values
nano secrets.yaml

# Apply (DO NOT commit secrets.yaml!)
kubectl apply -f secrets.yaml

# Delete the file immediately
rm secrets.yaml
```

### 3. Deploy with Helm

```bash
# Add MongoDB helm repo (if not already added)
helm repo add mongodb https://10gen.github.io/helm-charts
helm repo update

# Deploy to staging
helm upgrade --install github-copier mongodb/web-app \
  --namespace docs \
  --values values.yaml \
  --set image.tag=v0.2.0

# Check deployment status
kubectl get pods -n docs -l app=github-copier
kubectl logs -n docs -l app=github-copier --tail=50
```

### 4. Verify Deployment

```bash
# Check pod status
kubectl get pods -n docs -l app=github-copier

# Check logs
kubectl logs -n docs -l app=github-copier --tail=100 -f

# Test health endpoint (from within cluster or via port-forward)
kubectl port-forward -n docs svc/github-copier 8080:80

# In another terminal:
curl http://localhost:8080/health
curl http://localhost:8080/ready
curl http://localhost:8080/metrics
```

### 5. Test Webhook Endpoint

```bash
# Port-forward to test locally
kubectl port-forward -n docs svc/github-copier 8080:80

# Send test webhook (in another terminal)
cd /Users/cbullinger/devdocs/grove/github-copier
./cmd/test-webhook/test-webhook \
  -url http://localhost:8080/events \
  -secret "YOUR_WEBHOOK_SECRET" \
  -payload testdata/example-pr-merged.json
```

## Next Steps

Once the service is deployed and verified:

1. **Submit KANOPY Ticket** for vanity hostname with IP allowlist
   - Desired hostname: `github-webhooks.staging.corp.mongodb.com` (or similar)
   - Current endpoint: `github-copier.docs.staging.corp.mongodb.com`
   - IP allowlist: GitHub webhook IPs from https://api.github.com/meta
   - See `argo-events/KANOPY_INGRESS_OPTIONS.md` for details

2. **Configure GitHub Webhook**
   - Once vanity hostname is set up, update GitHub App webhook URL
   - Payload URL: `https://github-webhooks.staging.corp.mongodb.com/events`
   - Secret: (same as WEBHOOK_SECRET in Kubernetes secret)

3. **Monitor and Test**
   - Watch logs for incoming webhooks
   - Test with a real PR in a test repository
   - Verify files are copied correctly

## Troubleshooting

### Pods not starting

```bash
# Check pod events
kubectl describe pod -n docs -l app=github-copier

# Check logs
kubectl logs -n docs -l app=github-copier
```

### Secret not found errors

```bash
# Verify secrets exist
kubectl get secrets -n docs | grep github-copier

# Check secret contents (base64 encoded)
kubectl get secret github-copier-app-credentials -n docs -o yaml
```

### Image pull errors

```bash
# Verify image exists in ECR
docker pull 795250896452.dkr.ecr.us-east-1.amazonaws.com/docs/github-copier:latest

# Check if Kanopy has pull access (should be automatic for same AWS account)
kubectl describe pod -n docs -l app=github-copier | grep -A 5 "Events:"
```

## Cleanup

To remove the deployment:

```bash
# Uninstall helm release
helm uninstall github-copier -n docs

# Delete secrets (if needed)
kubectl delete secret github-copier-app-credentials -n docs
kubectl delete secret github-copier-pem -n docs
kubectl delete secret github-copier-webhook-secret -n docs
```

## References

- [Kanopy Documentation](https://kanopy.corp.mongodb.com)
- [web-app Helm Chart](https://github.com/10gen/helm-charts/tree/master/charts/web-app)
- [GitHub Copier Documentation](../../docs/)

