# Argo Events Testing for GitHub Copier

This directory contains Kubernetes manifests for testing Argo Events integration with GitHub webhooks in Kanopy staging.

## Setup Instructions

### 1. Create Webhook Secret

Create a Kubernetes secret with your GitHub webhook secret (at least 12 characters):

```bash
export KUBECONFIG=~/.kube/config.staging

# Generate a random webhook secret or use an existing one
WEBHOOK_SECRET="test-webhook-secret-12345"

kubectl create secret generic github-webhook-secret \
  --from-literal=secret=$WEBHOOK_SECRET \
  -n docs
```

### 2. Apply Resources

```bash
# EventBus (already applied)
kubectl apply -f eventbus.yaml

# Echo server for testing
kubectl apply -f echo-server.yaml

# GitHub EventSource
kubectl apply -f eventsource.yaml

# Sensor with HTTP trigger
kubectl apply -f sensor.yaml
```

### 3. Get Webhook URL

The webhook URL will be:
```
https://webhooks.staging.corp.mongodb.com/docs/github-copier-eventsource/events
```

Configure this URL in your GitHub App or repository webhook settings.

### 4. Monitor Events

Watch the echo server logs to see incoming webhook payloads:

```bash
kubectl logs -f deployment/echo-server -n docs
```

## Cleanup

```bash
kubectl delete -f sensor.yaml
kubectl delete -f eventsource.yaml
kubectl delete -f echo-server.yaml
kubectl delete -f eventbus.yaml
kubectl delete secret github-webhook-secret -n docs
```

