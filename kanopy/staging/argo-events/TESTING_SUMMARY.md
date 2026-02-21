# Argo Events Testing Summary

## ⚠️ TESTING COMPLETE - RESOURCES CLEANED UP

**Status:** All Argo Events testing resources have been deleted from the `docs` namespace (2024-02-21).

**Conclusion:** Argo Events is NOT recommended for GitHub webhooks. Use **Vanity Hostname with IP Allowlist** instead (see `KANOPY_INGRESS_OPTIONS.md`).

---

## What We Set Up (Now Deleted)

Successfully deployed Argo Events infrastructure in Kanopy staging (`docs` namespace):

### Components Deployed

1. **EventBus** (`default`)
   - 3 NATS pods running
   - Using native NATS (not JetStream) to avoid the reloader container bug
   - Status: ✅ Running

2. **EventSource** (`github-copier-eventsource`)
   - Configured for GitHub webhooks
   - Using "github" well-known source (no CORPSEC approval needed)
   - Listening on port 12000
   - Status: ✅ Deployed

3. **Sensor** (`github-copier-sensor`)
   - HTTP trigger configured
   - Forwards to echo-server at `http://echo-server.docs.svc.cluster.local:8080/events`
   - Using `useRawData: true` to pass payload unmodified
   - Status: ✅ Running

4. **Echo Server** (test deployment)
   - Python HTTP server that logs all incoming requests
   - Prints headers and body to stderr
   - Status: ✅ Running

## Webhook URL

The public webhook URL created by Kanopy:

```
https://webhooks.staging.corp.mongodb.com/docs/github-copier-eventsource/events
```

## Next Steps for Testing

### Phase 5: Configure GitHub Webhook

1. Go to your GitHub repository or GitHub App settings
2. Add a new webhook with:
   - **Payload URL**: `https://webhooks.staging.corp.mongodb.com/docs/github-copier-eventsource/events`
   - **Content type**: `application/json`
   - **Secret**: `test-webhook-secret-staging-12345`
   - **Events**: Select "Pull requests" and/or "Pushes"

### Phase 6: Test and Validate

1. **Trigger a test event** (create a PR, push a commit, etc.)

2. **Monitor the echo server logs**:
   ```bash
   export KUBECONFIG=~/.kube/config.staging
   kubectl logs -f deployment/echo-server -n docs
   ```

3. **Check EventSource logs** (if needed):
   ```bash
   kubectl logs -f deployment/github-copier-eventsource-eventsource-2cgz8 -n docs
   ```

4. **Check Sensor logs** (if needed):
   ```bash
   kubectl logs -f deployment/github-copier-sensor-sensor-cq875 -n docs
   ```

## Key Questions to Answer

From the testing, we have validated:

1. ✅ **Does the webhook reach the EventSource?** YES - EventSource receives and validates webhooks
2. ✅ **Does the EventSource validate the GitHub signature?** YES - webhook secret validation works
3. ❌ **Does the payload arrive at the echo server unmodified?** NO - payload is wrapped in `{"body": "..."}`
4. ❌ **Are GitHub headers (X-GitHub-Event, X-GitHub-Delivery, X-Hub-Signature-256) forwarded correctly?** NO - headers are merged into the body JSON
5. ❌ **Is the payload structure exactly as GitHub sends it, or is it wrapped/modified?** MODIFIED - double-encoded JSON string

## Critical Finding: Argo Events HTTP Trigger Limitation

**The Argo Events HTTP trigger does NOT support passing raw GitHub webhook payloads unmodified.**

### What We Observed:

When GitHub sends:
```json
{
  "action": "closed",
  "number": 123,
  "pull_request": { ... }
}
```

Argo Events HTTP trigger sends:
```json
{
  "body": "{\"action\":\"closed\",\"number\":123,\"pull_request\":{...}}"
}
```

### Problems:

1. **Payload is wrapped**: The original JSON is wrapped in a `{"body": "..."}` object
2. **Double-encoded**: The body is a JSON string, not a JSON object
3. **Headers merged into body**: GitHub headers are added to the JSON body instead of HTTP headers
4. **No raw passthrough**: Even with `useRawData: true`, the HTTP trigger constructs a JSON payload

### Impact on github-copier:

The `github-copier` application expects the exact GitHub webhook format. It cannot process:
- Wrapped payloads
- Double-encoded JSON strings
- Missing HTTP headers (X-GitHub-Event, X-Hub-Signature-256)

**This means Argo Events HTTP trigger is NOT suitable for forwarding GitHub webhooks to github-copier.**

## Known Issues Encountered

### Issue 1: EventBus Helm Chart Reloader Container Crash

**Problem**: The `mongodb/argo-eventbus` helm chart creates a `reloader` container that crashes with:
```
exec: "-pid": executable file not found in $PATH
```

**Solution**: Created EventBus manually using YAML instead of helm chart:
```yaml
apiVersion: argoproj.io/v1alpha1
kind: EventBus
metadata:
  name: default
spec:
  nats:
    native:
      replicas: 3
      auth: token
```

This matches the issue mentioned in the Slack conversation from the chat history.

### Issue 2: Sensor Payload Destination Validation

**Problem**: Argo Events validation webhook rejects empty `dest` in payload configuration:
```
parameter destination can't be empty
```

**Solution**: Changed from `dest: ""` to `dest: body`. Need to verify if this still passes raw data.

## Files Created

```
kanopy/staging/argo-events/
├── README.md                 # Setup instructions
├── TESTING_SUMMARY.md        # This file
├── eventbus.yaml             # EventBus resource (manual, not helm)
├── eventsource.yaml          # GitHub EventSource
├── sensor.yaml               # Sensor with HTTP trigger
└── echo-server.yaml          # Test echo server deployment
```

## Alternative Solutions

Since Argo Events HTTP trigger cannot pass raw GitHub webhooks, we have these options:

### Option 1: Vanity Hostname with IP Allowlist (RECOMMENDED)

- **Pros**:
  - Direct GitHub webhook → application (no transformation)
  - Simple, reliable, well-understood
  - No payload modification
  - Headers preserved
- **Cons**:
  - Requires CORPSEC security review
  - Takes time to get approved
- **Verdict**: This is the better path for github-copier

### Option 2: Custom Webhook Proxy

- Deploy a simple proxy service in Kanopy that:
  1. Receives GitHub webhooks via Argo Events
  2. Extracts the original payload from the wrapped format
  3. Reconstructs the original HTTP request with headers
  4. Forwards to github-copier
- **Pros**:
  - Self-service (no CORPSEC approval)
  - Can use Argo Events well-known source
- **Cons**:
  - Additional complexity
  - Another service to maintain
  - Potential for bugs in payload reconstruction
  - Adds latency

### Option 3: Modify github-copier to Accept Wrapped Payloads

- Change github-copier to handle both formats:
  - Original GitHub webhook format
  - Argo Events wrapped format
- **Pros**:
  - Works with Argo Events
- **Cons**:
  - Complicates the application code
  - Harder to test
  - Diverges from standard GitHub webhook handling
  - Still need to handle missing headers

## Recommendation

**Go with Option 1: Vanity Hostname with IP Allowlist**

The CORPSEC approval process is worth it for:
- Simplicity and reliability
- No payload transformation issues
- Standard GitHub webhook handling
- Less code to maintain

## Cleanup Commands

### ✅ Cleanup Complete (2024-02-21)

All resources have been deleted from the `docs` namespace:

```bash
# Commands executed:
kubectl delete sensor github-copier-sensor -n docs
kubectl delete eventsource github-copier-eventsource -n docs
kubectl delete deployment echo-server -n docs
kubectl delete service echo-server -n docs
kubectl delete eventbus default -n docs
kubectl delete secret github-webhook-secret -n docs
```

**Verification:**
```bash
$ kubectl get eventbus,eventsource,sensor -n docs
No resources found in docs namespace.
```

All Argo Events testing resources have been successfully removed.

