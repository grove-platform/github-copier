package services

import (
	"context"
	"strings"
)

// repoFilter post-filters operator UI rows so a writer only sees rows that
// reference repos they can read on GitHub. Operators (admin/maintain on the
// auth repo) bypass the check — they're trusted with full topology.
//
// Per-request only: results memoise inside one filter instance so we don't
// hit the GitHub API more than once per distinct repo per request, even with
// pages of audit/trace rows that share the same source/target.
//
// Construct via newRepoFilter and reuse for the duration of one HTTP handler.
type repoFilter struct {
	ctx   context.Context
	cache *ghAuthCache
	pat   string
	user  *OperatorUser
	// memo of repo → has-read for this request. nil entries are treated as
	// "deny" so a transient GitHub error fails closed for that row.
	memo map[string]bool
}

func newRepoFilter(ctx context.Context, cache *ghAuthCache, pat string, user *OperatorUser) *repoFilter {
	return &repoFilter{ctx: ctx, cache: cache, pat: pat, user: user, memo: make(map[string]bool)}
}

// bypass reports whether this filter should let every row through unmodified.
// Operators see everything; writers go through per-repo checks.
// When cache is nil (kanopy auth mode has no GitHub PAT), bypass so writers
// see all rows — there is no PAT available to call the GitHub permission API.
func (f *repoFilter) bypass() bool {
	return f == nil || f.user == nil || f.user.Role == RoleOperator || f.cache == nil
}

// canRead returns true if the caller has read access to repo. Empty repo
// returns true so a single missing field doesn't drop a row that's otherwise
// scoped by a populated peer (e.g. a copy event with target_repo set but
// source_repo empty). Callers that need both fields populated should compose
// canRead checks themselves and treat empty as "no info" rather than "ok".
func (f *repoFilter) canRead(repo string) bool {
	if f.bypass() {
		return true
	}
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return true
	}
	if v, ok := f.memo[repo]; ok {
		return v
	}
	ok, err := f.cache.CanUserReadRepo(f.ctx, f.pat, f.user.Login, repo)
	// Fail closed on error: a transient GitHub 5xx shouldn't reveal a row
	// the writer wouldn't normally see. Cached deny entries naturally pass
	// through the same path.
	allowed := err == nil && ok
	f.memo[repo] = allowed
	return allowed
}

// allowAuditEvent decides whether to surface one audit row to the caller.
// A writer must be able to read every repo named on the row; otherwise the
// row leaks the existence of a repo the writer has no business knowing about.
// Rows with neither source_repo nor target_repo populated (config_change
// events) are operator-only — writers don't get to see admin actions.
func (f *repoFilter) allowAuditEvent(ev *AuditEvent) bool {
	if f.bypass() {
		return true
	}
	if strings.TrimSpace(ev.SourceRepo) == "" && strings.TrimSpace(ev.TargetRepo) == "" {
		return false
	}
	if ev.SourceRepo != "" && !f.canRead(ev.SourceRepo) {
		return false
	}
	if ev.TargetRepo != "" && !f.canRead(ev.TargetRepo) {
		return false
	}
	return true
}

// filterAuditEvents returns the subset of events visible to the caller.
// Returns the input slice unchanged when the filter is in bypass mode.
func (f *repoFilter) filterAuditEvents(events []AuditEvent) []AuditEvent {
	if f.bypass() {
		return events
	}
	out := make([]AuditEvent, 0, len(events))
	for i := range events {
		if f.allowAuditEvent(&events[i]) {
			out = append(out, events[i])
		}
	}
	return out
}

// filterWebhookTraces returns the subset of webhook traces visible to the
// caller. Traces with no Repo populated are dropped for writers — a trace
// without a repo can't be scoped, and the Detail field can carry sensitive
// content (truncation is a length cap, not redaction).
func (f *repoFilter) filterWebhookTraces(traces []WebhookTraceEntry) []WebhookTraceEntry {
	if f.bypass() {
		return traces
	}
	out := make([]WebhookTraceEntry, 0, len(traces))
	for _, t := range traces {
		if strings.TrimSpace(t.Repo) == "" {
			continue
		}
		if f.canRead(t.Repo) {
			out = append(out, t)
		}
	}
	return out
}
