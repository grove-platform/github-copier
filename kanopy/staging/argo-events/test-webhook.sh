#!/bin/bash
# Test script to send a sample GitHub webhook payload to the EventSource

set -e

WEBHOOK_SECRET="test-webhook-secret-staging-12345"
WEBHOOK_URL="https://webhooks.staging.corp.mongodb.com/docs/github-copier-eventsource/events"

# Sample GitHub pull_request webhook payload
PAYLOAD='{
  "action": "closed",
  "number": 123,
  "pull_request": {
    "id": 1,
    "number": 123,
    "state": "closed",
    "merged": true,
    "title": "Test PR",
    "user": {
      "login": "testuser"
    },
    "head": {
      "ref": "test-branch",
      "sha": "abc123"
    },
    "base": {
      "ref": "main",
      "sha": "def456"
    }
  },
  "repository": {
    "name": "docs-realm",
    "full_name": "mongodb/docs-realm",
    "owner": {
      "login": "mongodb"
    }
  }
}'

# Calculate HMAC signature (GitHub uses HMAC-SHA256)
SIGNATURE=$(echo -n "$PAYLOAD" | openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" | sed 's/^.* //')

echo "Sending test webhook to: $WEBHOOK_URL"
echo "Payload: $PAYLOAD"
echo "Signature: sha256=$SIGNATURE"
echo ""

# Send the webhook
curl -X POST "$WEBHOOK_URL" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Event: pull_request" \
  -H "X-GitHub-Delivery: test-delivery-$(date +%s)" \
  -H "X-Hub-Signature-256: sha256=$SIGNATURE" \
  -d "$PAYLOAD" \
  -v

echo ""
echo "Webhook sent! Check the echo server logs:"
echo "  export KUBECONFIG=~/.kube/config.staging"
echo "  kubectl logs -f deployment/echo-server -n docs"

