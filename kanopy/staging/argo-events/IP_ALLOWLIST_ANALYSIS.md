# IP Allowlist Options in Kanopy - Analysis

## Question

Can we add an IP allowlist to the default hostname (`github-copier.docs.staging.corp.mongodb.com`) without requesting a vanity hostname?

## Answer: Technically Yes, But Not Recommended

### What I Found

**Istio AuthorizationPolicy with `ipBlocks`** can be used to restrict access by source IP, but there are important caveats.

From the Kanopy docs (`peerauthentication.md`):

```yaml
apiVersion: security.istio.io/v1beta1
kind: AuthorizationPolicy
metadata:
  name: my-service-allowlist
  namespace: docs
spec:
  action: ALLOW
  selector:
    matchLabels:
      app: github-copier
  rules:
  - from:
    - source:
        ipBlocks: ["192.30.252.0/22", "185.199.108.0/22", ...]
```

### The Problem: `ipBlocks` vs `remoteIpBlocks`

Istio has TWO different IP matching fields:

1. **`ipBlocks`** - Matches the **peer IP** (the immediate connection source)
   - For external traffic through a load balancer, this will be the **load balancer IP**, NOT the original client IP
   - ❌ Won't work for GitHub webhooks coming through Kanopy's ingress gateway

2. **`remoteIpBlocks`** - Matches the **original client IP** from X-Forwarded-For headers
   - This is what we need for external webhooks
   - ⚠️ Not documented in Kanopy docs
   - ⚠️ May not be supported or may require special configuration

### Why This Isn't Documented as Self-Service

The Kanopy documentation **only mentions IP allowlisting in the context of vanity hostnames**:

> "For TCP services that need to be accessible from outside the Kanopy cluster please submit a KANOPY ticket requesting a vanity hostname, **specifying any IP whitelists** that are applicable to your application."

This suggests that:
1. IP allowlisting for external access is **not self-service**
2. It requires Kanopy team involvement (via ticket)
3. It's tied to the vanity hostname request process

### Why Kanopy Team Involvement Is Needed

Proper IP allowlisting for external webhooks likely requires:

1. **Load balancer configuration** - AWS ALB/NLB rules to preserve source IPs
2. **Istio Gateway configuration** - Proper X-Forwarded-For handling
3. **AuthorizationPolicy with `remoteIpBlocks`** - Not `ipBlocks`
4. **Testing and validation** - Ensure GitHub IPs are correctly identified

These are infrastructure-level changes that the Kanopy team manages, not self-service configurations.

## Recommendation: Submit KANOPY Ticket

### Why Submit a Ticket (Even Without Vanity Hostname)

You could submit a KANOPY ticket asking:

> "Can we add an IP allowlist to our default hostname `github-copier.docs.staging.corp.mongodb.com` to restrict access to GitHub webhook IPs only?"

**Possible outcomes:**

1. ✅ **They say yes** - Great! They'll configure it for you
2. ⚠️ **They say "you need a vanity hostname for that"** - Then you request one
3. 💡 **They suggest an alternative** - Maybe there's another pattern

### Why This Is Better Than DIY

**If you try to configure AuthorizationPolicy yourself:**
- ❌ `ipBlocks` will match load balancer IPs, not GitHub IPs
- ❌ `remoteIpBlocks` may not work without proper infrastructure setup
- ❌ You might accidentally block all traffic
- ❌ You might think it's working but it's not actually enforcing the allowlist

**If you submit a ticket:**
- ✅ Kanopy team knows the correct configuration
- ✅ They'll set up load balancer and gateway properly
- ✅ They'll test it works correctly
- ✅ You get support if something breaks

## Comparison: Ticket vs DIY

| Approach | Effort | Risk | Reliability |
|----------|--------|------|-------------|
| **Submit KANOPY ticket** | Low (fill out form) | Low | High (Kanopy team configures) |
| **DIY AuthorizationPolicy** | Medium (research, test) | High (might not work) | Low (untested pattern) |

## Conclusion

**My original suggestion was wrong.** I thought you could self-service an IP allowlist using AuthorizationPolicy, but after researching the docs and source code:

1. **IP allowlisting for external access is NOT self-service** in Kanopy
2. **It requires Kanopy team involvement** (via ticket)
3. **It may or may not require a vanity hostname** - that's up to the Kanopy team

### Recommended Approach

**Submit a KANOPY ticket** with one of these two requests:

**Option A: Ask for IP allowlist on default hostname**
```
Subject: IP Allowlist for Default Hostname

Can we add an IP allowlist to our default hostname 
`github-copier.docs.staging.corp.mongodb.com` to restrict 
access to GitHub webhook IPs only?

Service: github-copier (webhook receiver)
Namespace: docs
Environment: staging
IP Allowlist: GitHub webhook IPs from https://api.github.com/meta
```

**Option B: Request vanity hostname with IP allowlist** (safer bet)
```
Subject: Vanity Hostname with IP Allowlist

Request for vanity hostname with IP allowlist for GitHub webhook receiver.

Desired hostname: github-webhooks.staging.corp.mongodb.com
Current endpoint: github-copier.docs.staging.corp.mongodb.com
IP Allowlist: GitHub webhook IPs from https://api.github.com/meta
...
```

I recommend **Option B** because:
- It's the documented pattern
- Clear expectations
- Kanopy team knows exactly what to do
- More likely to be approved quickly

## References

- Kanopy Ingress Docs: Mentions IP allowlist only with vanity hostnames
- Kanopy PeerAuthentication Docs: Shows `ipBlocks` for internal IPs only
- Istio AuthorizationPolicy: Supports `remoteIpBlocks` but not documented in Kanopy

