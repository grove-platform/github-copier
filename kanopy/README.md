# Kanopy Deployment Configuration

This directory contains Kanopy deployment configurations for github-copier.

## Structure

```
kanopy/
├── README.md                    # This file
├── DEPLOYMENT-GUIDE.md          # Step-by-step deployment guide
├── staging/
│   ├── README.md                # Staging-specific documentation
│   ├── values.yaml              # Helm chart values for staging
│   ├── secrets.yaml.template    # Template for Kubernetes secrets
│   └── argo-events/             # Argo Events testing artifacts (historical)
└── production/                  # Production config (to be created)
```

## Quick Start

### First-Time Deployment

Follow the [Deployment Guide](DEPLOYMENT-GUIDE.md) for complete step-by-step instructions.

**TL;DR:**
1. Build and push Docker image to Artifactory
2. Create Kubernetes secrets in `docs` namespace
3. Deploy with Helm using `staging/values.yaml`
4. Verify deployment
5. Submit KANOPY ticket for vanity hostname with IP allowlist
6. Configure GitHub webhook once hostname is ready

### Subsequent Deployments

```bash
# Update image tag in values.yaml or override via --set
helm upgrade github-copier mongodb/web-app \
  --namespace docs \
  --values kanopy/staging/values.yaml \
  --set image.tag=v0.3.0
```

## Environments

### Staging

- **Namespace:** docs
- **Cluster:** api.staging.corp.mongodb.com
- **Default Hostname:** `github-copier.docs.staging.corp.mongodb.com`
- **Vanity Hostname:** `github-webhooks.staging.corp.mongodb.com` (pending KANOPY ticket)
- **Config:** [staging/values.yaml](staging/values.yaml)
- **Docs:** [staging/README.md](staging/README.md)

### Production

- **Status:** Not yet deployed
- **Namespace:** docs
- **Cluster:** api.prod.corp.mongodb.com
- **Config:** To be created in `production/values.yaml`

## Key Differences from Cloud Run

| Aspect | Cloud Run | Kanopy |
|--------|-----------|--------|
| **Secrets** | GCP Secret Manager | Kubernetes Secrets |
| **Logging** | Google Cloud Logging | stdout/stderr → Kanopy logging |
| **Metrics** | Cloud Monitoring | Prometheus (scraped from /metrics) |
| **Ingress** | Cloud Run URL | Istio Gateway + VirtualService |
| **Scaling** | Serverless (0-N) | HPA (1-N replicas) |
| **Environment Variables** | `env-cloudrun.yaml` | `values.yaml` (env + envSecrets) |

## Secret Management

### Secret Naming Convention

All secrets are prefixed with `github-copier-` to avoid collisions in the shared `docs` namespace:

- `github-copier-app-credentials` - GitHub App ID and Installation ID
- `github-copier-pem` - GitHub App private key (PEM file)
- `github-copier-webhook-secret` - Webhook signature validation secret

### Creating Secrets

```bash
# App credentials
kubectl create secret generic github-copier-app-credentials \
  --from-literal=GITHUB_APP_ID="123456" \
  --from-literal=INSTALLATION_ID="789012" \
  -n docs

# PEM key
kubectl create secret generic github-copier-pem \
  --from-file=GITHUB_APP_PRIVATE_KEY=/path/to/app.pem \
  -n docs

# Webhook secret
kubectl create secret generic github-copier-webhook-secret \
  --from-literal=WEBHOOK_SECRET="$(openssl rand -hex 32)" \
  -n docs
```

See [staging/README.md](staging/README.md) for detailed instructions.

## CI/CD

### Drone Pipeline

The `.drone.yml` file in the project root defines the CI/CD pipeline:

1. **Test** - Run unit tests
2. **Build and Push** - Build Docker image and push to Artifactory
3. **Deploy Staging** - Auto-deploy to staging on push to `main` or `kanopy-deployment`
4. **Deploy Production** - Manual deploy on git tags (`v*`)

### Required Secrets

Configure in Drone: https://drone.corp.mongodb.com/grove-platform/github-copier/settings

- `artifactory_username` - Your Artifactory username
- `artifactory_password` - Your Artifactory API token

## Monitoring

### Health Checks

- **Liveness:** `GET /health` - Returns 200 if app is running
- **Readiness:** `GET /ready` - Returns 200 if app is ready to receive traffic

### Metrics

Prometheus metrics are exposed at `GET /metrics`:

```bash
# Port-forward to access metrics
kubectl port-forward -n docs svc/github-copier 8080:80
curl http://localhost:8080/metrics
```

Metrics are automatically scraped by Kanopy's Prometheus (via pod annotations).

### Logs

```bash
# View logs
kubectl logs -n docs -l app=github-copier --tail=100 -f

# View logs for specific pod
kubectl logs -n docs github-copier-<pod-id> -f
```

## Troubleshooting

### Common Issues

1. **Pods not starting** - Check secrets exist and are correctly named
2. **Image pull errors** - Verify Artifactory credentials and image exists
3. **Webhook 404 errors** - Vanity hostname not yet configured (use port-forward for testing)
4. **Secret access errors** - Ensure `SKIP_SECRET_MANAGER=true` is set

See [staging/README.md](staging/README.md) for detailed troubleshooting.

## References

- [Deployment Guide](DEPLOYMENT-GUIDE.md) - Complete deployment walkthrough
- [Staging README](staging/README.md) - Staging environment details
- [Kanopy Documentation](https://kanopy.corp.mongodb.com)
- [web-app Helm Chart](https://github.com/10gen/helm-charts/tree/master/charts/web-app)
- [Ingress Options Analysis](staging/argo-events/KANOPY_INGRESS_OPTIONS.md)

