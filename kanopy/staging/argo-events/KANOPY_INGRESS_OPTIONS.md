# Kanopy Ingress Options for GitHub Webhooks

## Summary of Findings

Based on the Kanopy documentation, there are **THREE** options for receiving external webhooks:

1. ✅ **Vanity Hostname with IP Allowlist** (AVAILABLE - No CORPSEC approval needed!)
2. ⚠️ **Argo Events** (Tested - Has payload transformation issues)
3. ❌ **Public Hostname** (Not applicable - requires security review + marketing approval)

## Option 1: Vanity Hostname with IP Allowlist ✅ RECOMMENDED

### What It Is

From the Kanopy ingress documentation:

> **Self-service capabilities at the edge are currently limited to HTTP services. For TCP services that need to be accessible from outside the Kanopy cluster please submit a [KANOPY ticket](https://jira.mongodb.org/plugins/servlet/desk/portal/48) requesting a vanity hostname, specifying any IP whitelists that are applicable to your application.**

This means you CAN request a vanity hostname for HTTP services (like webhooks) with IP allowlisting!

### Key Points

- **No CORPSEC approval required** - Just submit a KANOPY ticket
- **IP allowlist supported** - GitHub's webhook IPs can be allowlisted
- **Direct connection** - No payload transformation
- **Standard Kanopy service** - Well-supported, documented process

### GitHub Webhook IPs

GitHub publishes their webhook source IPs at: https://api.github.com/meta

Current webhook IP ranges (as of 2024):
```
192.30.252.0/22
185.199.108.0/22
140.82.112.0/20
143.55.64.0/20
2a0a:a440::/29
2606:50c0::/32
```

### Process

1. Submit a [KANOPY ticket](https://jira.mongodb.org/plugins/servlet/desk/portal/48)
2. Request: "Vanity hostname for HTTP webhook service with IP allowlist"
3. Specify:
   - Desired hostname (e.g., `github-webhooks.corp.mongodb.com` or similar)
   - IP allowlist: GitHub webhook IPs from https://api.github.com/meta
   - Service details: github-copier webhook endpoint
   - Namespace: `docs`
   - Environment: `staging` (and later `prod`)

4. Kanopy team configures the ingress with IP restrictions
5. Update GitHub webhook configuration to use the new URL
6. Done!

### Advantages

- ✅ No code changes needed
- ✅ No custom proxy to maintain
- ✅ No payload transformation issues
- ✅ Direct GitHub → app connection
- ✅ Standard Kanopy pattern
- ✅ No CORPSEC approval process
- ✅ IP allowlist provides security

### Disadvantages

- ⏱️ Requires KANOPY ticket (turnaround time unknown, likely days not weeks)
- 📋 Manual process (not self-service)
- 🔄 Need to update IP allowlist if GitHub changes their ranges (rare)

## Option 2: Argo Events ⚠️ NOT RECOMMENDED

### Status: Tested - Has Critical Issues

We tested this thoroughly and found:

**What Works:**
- ✅ EventBus deployment (with manual NATS config, not helm chart)
- ✅ EventSource receives webhooks
- ✅ Signature validation works
- ✅ Sensor triggers HTTP requests

**What Doesn't Work:**
- ❌ Payload transformation - Argo Events wraps payload in `{"body": "..."}`
- ❌ JSON double-encoding - Payload becomes a string instead of object
- ❌ Headers merged into body - GitHub headers not sent as HTTP headers
- ❌ External webhook URL returns 404 (routing not created despite well-known source annotation)

### Workaround: Custom Webhook Proxy

**Effort:** 2-5 days of development + ongoing maintenance

**Complexity:** Medium

**Risks:**
- Signature validation timing (potential showstopper)
- Maintenance burden
- Extra point of failure
- Performance overhead

See `WEBHOOK_PROXY_ANALYSIS.md` for detailed analysis.

### Conclusion

Argo Events is **not suitable** for GitHub webhooks without a custom proxy, and even with a proxy it adds significant complexity for questionable benefit.

## Option 3: Public Hostname ❌ NOT APPLICABLE

From the custom hostnames documentation:

> Public endpoints for applications running in Kanopy are not allowed by default.
>
> If your application is customer facing and can not be behind corpsecure or accessible exclusively via VPN, you'll need to apply for an exception before we can make it public.
>
> Public hostname exceptions are considered on a case-by-case basis, require a security review by infosec, and approval from the marketing SEO team.

**Why this doesn't apply:**
- github-copier is an internal tool, not customer-facing
- Requires CORPSEC security review
- Requires marketing SEO team approval
- Intended for `*.mongodb.com` public endpoints
- Overkill for webhook ingress

## Recommendation

### Use Option 1: Vanity Hostname with IP Allowlist

**Why:**
1. **No CORPSEC approval needed** - The original concern was that vanity hostnames require CORPSEC approval, but the docs show you can request them via KANOPY ticket with IP allowlists
2. **Simplest solution** - No code changes, no custom proxy, no maintenance
3. **Most reliable** - Direct connection, no payload transformation
4. **Standard pattern** - Well-supported by Kanopy team
5. **Secure** - IP allowlist restricts access to GitHub's webhook IPs only

**Next Steps:**
1. Submit KANOPY ticket requesting vanity hostname with GitHub IP allowlist
2. Wait for Kanopy team to configure (likely a few days)
3. Update GitHub webhook configuration
4. Test with staging environment
5. Repeat for production when ready

## References

- Kanopy Ingress Docs: `/Users/cbullinger/dev/reference/kanopy-docs/docs/ingress/README.md`
- Custom Hostnames: `/Users/cbullinger/dev/reference/kanopy-docs/docs/production/custom_hostnames.md`
- Argo Events Testing: `kanopy/staging/argo-events/TESTING_SUMMARY.md`
- GitHub Webhook IPs: https://api.github.com/meta
- KANOPY Service Desk: https://jira.mongodb.org/plugins/servlet/desk/portal/48

