# PR #7 Review: `feat(operator): operator UI, GitHub PAT auth, AI rule suggester`

**Repo:** grove-platform/github-copier
**PR:** https://github.com/grove-platform/github-copier/pull/7
**Branch:** `feat/operator-ui-audit` → `main`
**Scope:** +5,373 / −73 across 28 files
**Reviewer perspective:** experienced fullstack, webserver-app focus

## Overview

This PR ships a major v0.4.0 milestone: a 5-tab operator UI backed by GitHub PAT auth with role derivation, webhook replay with per-repo permission checks, and an AI rule suggester with swappable Ollama/Anthropic providers through the Grove Foundry APIM gateway. One ~4,000 line embedded HTML bundle carries most of the frontend.

The architecture is sound: `LLMClient` interface with per-provider implementations, self-verifying LLM output against the in-process `PatternMatcher`, fail-closed config validation (`OPERATOR_UI_ENABLED` + `OPERATOR_AUTH_REPO`), and Secret Manager loading for the Anthropic key. The `#nosec` annotations are justified — base URLs are operator-controlled, not user-controlled.

---

## Blocking / High-priority issues

### 1. Permission-check soft-fail grants writer role by default — auth bypass risk
**File:** `services/operator_auth.go:186-190`

```go
if err != nil {
    LogWarning("GitHub permission check failed, defaulting to writer role", ...)
    return user, nil
}
```

The PR description's spec says *"None / 404 → denied → 401 Unauthorized"*, but the code returns **writer** on any error from `ghAPIGetRepoPermission` — including GitHub's 404 for "not a collaborator". Combined with `--allow-unauthenticated` on the Cloud Run service, **any** valid GitHub PAT lets a caller view audit logs, webhook traces, workflows, logs drawer, and invoke the AI suggester (which costs real Anthropic tokens).

**Fix:** on 404, return `RoleDenied` + error; only soft-fail on transient 5xx. Distinguish the two cases in `ghAPIGetRepoPermission` by inspecting the status code.

### 2. LLM cost exposure — `/suggest-rule` is unbounded per user
**File:** `services/operator_suggest_rule.go:55`

Any authenticated writer can call `/operator/api/suggest-rule` with no per-user rate limit. Combined with issue #1, this is effectively "anyone with a GitHub PAT can spend Anthropic credits." Add a per-token token bucket (even generous, e.g. 30/hour) before release.

### 3. `/llm/status` calls real Anthropic `/v1/messages` on every refresh
**File:** `services/llm_anthropic.go:105-131`

The ping uses `max_tokens=1` but still hits the Messages API (one input + one output token) on every status tab refresh (writers poll this for the status badge). Cache the last successful ping for ~30s, or degrade to a cheap HEAD/OPTIONS against the gateway.

### 4. Raw PATs stored as map keys
**File:** `services/operator_auth.go:49`, `services/operator_auth.go:104`

`entries[token]` and `repoPerm[token+"\x00"+repo]` keep full PATs live in the heap for the 5-min TTL. A memory dump (crash, Cloud Run profile, goroutine dump) would leak every active operator's token. Hash with SHA-256 and key the maps by digest — same security semantics, no plaintext secrets in process memory.

---

## Medium — worth addressing before merge

### 5. Global LLM mutation surprises multi-operator use
**File:** `services/operator_llm_admin.go:84-88`

`SetActiveModel`/`SetBaseURL` mutate the shared `o.llm` client. Two operators on the UI at once will clobber each other's model choice, and changes silently revert on restart. Either document this as "last write wins, ephemeral" in the UI or make the active model per-request.

### 6. Test coverage gap on the new security-critical surface

The diff adds ~3 test files (audit, delivery tracker, github_write_to_target) but **zero tests** for:
- `services/operator_auth.go`
- `services/operator_ui.go`
- `services/operator_suggest_rule.go`
- `services/llm_anthropic.go`
- `services/llm_client.go`
- `services/operator_llm_admin.go`

At minimum, add table-driven tests for `validateGitHubPAT` role mapping (including the 404 case from issue #1) and `verifySuggestedRule` for each transform type (move/copy/glob/regex).

### 7. `handleRepoPermission` swallows the real error
**File:** `services/operator_ui.go:225`

```go
canRead, _ := o.ghCache.CanUserReadRepo(ctx, userPAT, user.Login, repo)
```

A GitHub rate-limit or 5xx is indistinguishable from "no access" to the frontend, so users see disabled replay buttons with no indication why. Return a per-repo `{allowed: bool, error?: string}` shape instead of `map[string]bool`.

### 8. `githubCreateVersionTag` path components aren't escaped
**File:** `services/operator_ui.go:805`, `services/operator_ui.go:842`

Unlike `ghAPIGetRepoPermission`, this path uses raw `fmt.Sprintf` with `owner`, `repo`, `baseBranch`. The inputs are trusted (env vars), but apply the same `ghUsernameRe`/`ghRepoNameRe` + `url.PathEscape` treatment for defense-in-depth and consistency — you already established the pattern one file over.

### 9. Anthropic fallback model list is a maintenance hazard
**File:** `services/llm_anthropic.go:26-30`

Hard-coding `claude-opus-4-7`, `claude-sonnet-4-6`, `claude-haiku-4-5-20251001` in the fallback means when those rotate you'll ship dead options to the UI. Either load from an embedded config file or trim to a single known-stable haiku alias.

### 10. Model name inconsistency across configs
- `configs/environment.go:281` → `claude-haiku-4-5-20251001`
- `.github/workflows/ci.yml` → `claude-haiku-4-5`

Both resolve via Anthropic aliasing, but pick one. The dated form is more reproducible; the alias drifts.

---

## Minor / nits

- `services/operator_ui.go:873-875`: `githubHTTPClient()` allocates a new `*http.Client` per call. Make it a package var.
- `services/operator_auth.go:93-100` + `services/operator_auth.go:123-130`: cache eviction is O(n) inside the write lock; fine at 100/500, just worth knowing.
- `services/web/operator/index.html` at ~4k lines is hard to review and diff. Splitting CSS/JS into sibling files and using `//go:embed web/operator/*` would make this reviewable and cacheable.
- `services/operator_ui.go:461-462`: `releaseMode` is only `"disabled"` vs `"tag_create_enabled"` — consider a typed enum.
- `services/operator_suggest_rule.go:344-349`: `truncate(s, n)` counts bytes not runes — cuts inside multi-byte glyphs. Use `utf8.RuneCountInString`-bounded slicing.
- `services/operator_ui.go:738`: `operator_replay` deliveryID uses `time.Now().UnixMilli()` — two replays in the same ms collide. Add a short random suffix.

---

## What's solid

- **SSRF hardening** in `ghAPIGetRepoPermission` is textbook — whitelist regex + `url.PathEscape` + pinned host.
- **Fail-closed startup** in `validateOperatorAuth` correctly prevents the "UI enabled but no auth repo" footgun.
- **Self-verification** of LLM output via `PatternMatcher` before showing to the user is the right pattern — catches hallucinated rules before they ship.
- **In-flight dedup** on replay + per-repo permission gating on the source repo is a well-considered authorization model.
- **Streaming NDJSON** for Ollama pulls with both request-context cancel and 20-min safety timeout is handled correctly.
- Scoping `write` → `writer` (not `operator`) based on real docs-repo access patterns is exactly the kind of call you want a human to make, and the comment explaining it is excellent.
- **Dual-header auth** (`x-api-key` + `api-key`) lets one client work against native Anthropic or the Azure APIM gateway — clean solution to a real deployment-flexibility problem.

---

## Recommendation

**Request changes** — items 1 and 2 are release-blocking for an `--allow-unauthenticated` Cloud Run deploy. Items 3, 4, and 6 are strongly recommended before cutting v0.4.0. The rest are polish.

---

## For the next agent

If you're picking this up to address the findings, suggested order:

1. Fix issue #1 first — distinguish 404 from transient errors in `ghAPIGetRepoPermission`, return `RoleDenied` on 404. Add a test that asserts the 404 path returns denied.
2. Add the token bucket for #2 (keyed by SHA-256 of PAT).
3. While in `operator_auth.go`, do #4 (hash keys) in the same pass.
4. Cache the `/llm/status` ping (#3) — 30s TTL stored on the `operatorUI` struct.
5. Backfill tests (#6) covering the new paths.
6. Sweep the mediums/minors.

The PR description's "Authentication & authorization" table is the source of truth for the intended role mapping — the code should match it exactly.
