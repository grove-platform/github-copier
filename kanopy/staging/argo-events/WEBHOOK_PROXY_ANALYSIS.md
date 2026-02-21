# Custom Webhook Proxy - Complexity Analysis

## Overview

A custom webhook proxy to unwrap Argo Events payloads and forward them as raw GitHub webhooks to github-copier.

## Scope of Work

### 1. Core Proxy Service (~100-150 lines)

**What it does:**
- Receives Argo Events wrapped payload: `{"body": "{...}"}`
- Unwraps and decodes the double-encoded JSON
- Extracts GitHub headers that were merged into the body
- Reconstructs the original HTTP request
- Forwards to github-copier

**Complexity:** Low - straightforward HTTP proxy logic

**See:** `webhook-proxy-sketch.go` for a rough implementation

### 2. Deployment Configuration (~50-100 lines)

**Files needed:**
- `Dockerfile` - containerize the proxy
- `deployment.yaml` - Kubernetes deployment
- `service.yaml` - Kubernetes service
- `helm/values.yaml` - if using Helm (recommended for Kanopy)

**Complexity:** Low - standard Kanopy deployment pattern

### 3. Update Argo Events Sensor (~10 lines)

**Change:**
```yaml
# Before: Forward to github-copier directly
url: http://github-copier.docs.svc.cluster.local:8080/webhook

# After: Forward to proxy
url: http://webhook-proxy.docs.svc.cluster.local:8080/webhook
```

**Complexity:** Trivial

### 4. Testing (~2-4 hours)

**What to test:**
- Proxy correctly unwraps payloads
- Headers are properly reconstructed
- github-copier receives correct format
- Error handling (malformed payloads, network errors)
- End-to-end with real GitHub webhooks

**Complexity:** Medium - need to verify exact GitHub webhook format

### 5. Monitoring & Observability (~50-100 lines)

**Add:**
- Prometheus metrics (requests, errors, latency)
- Structured logging
- Health check endpoint
- Readiness/liveness probes

**Complexity:** Low - standard patterns

## Total Effort Estimate

### Optimistic: 1-2 days
- Core proxy: 2-3 hours
- Deployment config: 1-2 hours
- Testing: 2-3 hours
- Documentation: 1 hour

### Realistic: 2-3 days
- Core proxy: 3-4 hours (with proper error handling)
- Deployment config: 2-3 hours (Helm chart, CI/CD)
- Testing: 4-6 hours (thorough testing, edge cases)
- Monitoring: 2-3 hours
- Documentation: 1-2 hours

### Pessimistic: 4-5 days
- Debugging edge cases: +1 day
- GitHub webhook format quirks: +1 day
- Kanopy deployment issues: +0.5 day

## Risks & Unknowns

### 🔴 High Risk

1. **GitHub webhook format variations**
   - Different event types might have different structures
   - Headers might be in different formats
   - Need to handle all event types github-copier expects

2. **Signature validation**
   - The `X-Hub-Signature-256` header is calculated on the ORIGINAL payload
   - If Argo Events modifies the payload before we see it, the signature won't match
   - **This could be a showstopper** - need to verify if EventSource validates signature BEFORE modifying payload

3. **Header extraction reliability**
   - Assuming headers are merged into body as top-level fields
   - What if there's a naming collision? (e.g., GitHub payload has an "X-GitHub-Event" field)
   - Need to verify exact Argo Events behavior

### 🟡 Medium Risk

4. **Maintenance burden**
   - Another service to deploy, monitor, and maintain
   - Need to update if Argo Events changes behavior
   - Need to update if GitHub changes webhook format

5. **Performance**
   - Extra hop adds latency (~10-50ms)
   - Extra point of failure
   - Need to handle high webhook volume

### 🟢 Low Risk

6. **Deployment complexity**
   - Standard Kanopy deployment
   - Well-understood patterns

## Critical Question: Signature Validation

**NEED TO VERIFY:** Does the Argo Events GitHub EventSource validate the webhook signature BEFORE or AFTER modifying the payload?

**If BEFORE:** ✅ Proxy will work - signature is already validated by EventSource

**If AFTER:** ❌ Proxy won't work - github-copier will reject the modified payload because signature won't match

**How to test:**
1. Send a webhook with an invalid signature to the EventSource
2. Check if it's rejected or forwarded to the Sensor
3. If rejected → signature validated before modification → proxy will work
4. If forwarded → signature validated after → proxy won't work

## Comparison: Proxy vs. Vanity Hostname

| Factor | Custom Proxy | Vanity Hostname |
|--------|--------------|-----------------|
| **Development time** | 2-5 days | 0 days |
| **CORPSEC approval** | Not needed | Required (~1-2 weeks?) |
| **Complexity** | Medium | Low |
| **Maintenance** | Ongoing | None |
| **Reliability** | Extra hop, extra failure point | Direct connection |
| **Signature validation** | Potential issue | No issue |
| **Total time to production** | 2-5 days | 1-3 weeks |

## Recommendation

### If you need this working THIS WEEK:
→ Build the custom proxy (but verify signature validation first!)

### If you can wait 1-3 weeks:
→ Go with vanity hostname (cleaner, more reliable long-term)

### Hybrid approach:
1. Start CORPSEC approval process for vanity hostname
2. Build custom proxy as a temporary solution
3. Switch to vanity hostname when approved
4. Deprecate proxy

This gives you:
- ✅ Working solution quickly
- ✅ Clean long-term solution
- ❌ Extra work building proxy that will be deprecated

## Next Steps

If you want to pursue the proxy approach:

1. **First:** Test signature validation behavior
   ```bash
   # Send webhook with invalid signature
   # Check EventSource logs to see if it's rejected
   ```

2. **If signature validation works:** Build the proxy
   - Start with the sketch in `webhook-proxy-sketch.go`
   - Add proper error handling
   - Add tests
   - Deploy to staging

3. **Test thoroughly** with real GitHub webhooks

4. **Monitor** for any edge cases or issues

