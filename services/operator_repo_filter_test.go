package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/types"
)

// stubGitHubPerRepo replaces githubAPIBaseURL with a server that hands out
// different permissions per repo so we can simulate writers who can read
// some repos but not others. perms maps "owner/repo" → permission string;
// repos missing from the map respond with HTTP 404 (no access).
func stubGitHubPerRepo(t *testing.T, perms map[string]string) (cleanup func(), apiCalls *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		// /repos/{owner}/{repo}/collaborators/{username}/permission
		path := r.URL.Path
		if !strings.HasPrefix(path, "/repos/") || !strings.HasSuffix(path, "/permission") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// Strip "/repos/" prefix and "/collaborators/.../permission" suffix
		stripped := strings.TrimPrefix(path, "/repos/")
		// stripped is "owner/repo/collaborators/user/permission"
		parts := strings.Split(stripped, "/")
		if len(parts) < 5 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		repo := parts[0] + "/" + parts[1]
		perm, ok := perms[repo]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"permission":%q}`, perm)
	}))
	prev := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	cleanup = func() {
		githubAPIBaseURL = prev
		srv.Close()
	}
	return cleanup, &calls
}

// fakeAuditLogger embeds NoOpAuditLogger and returns a fixed event slice.
type fakeAuditLogger struct {
	NoOpAuditLogger
	events []AuditEvent
}

func (f *fakeAuditLogger) QueryAuditEvents(_ context.Context, _ AuditListQuery) ([]AuditEvent, error) {
	out := make([]AuditEvent, len(f.events))
	copy(out, f.events)
	return out, nil
}

// requestAs builds a GET request whose context carries an authenticated
// operator user with the given role + login, and an Authorization header
// the handlers can read for repo permission checks.
func requestAs(role OperatorRole, login, path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer pat-"+login)
	ctx := context.WithValue(r.Context(), operatorUserCtxKey{}, &OperatorUser{Login: login, Role: role})
	return r.WithContext(ctx)
}

// ── repoFilter unit tests ──

func TestRepoFilter_OperatorBypassesAllChecks(t *testing.T) {
	cleanup, calls := stubGitHubPerRepo(t, nil) // no repos allowed
	defer cleanup()
	cache := newGHAuthCache(5 * time.Minute)
	f := newRepoFilter(context.Background(), cache, "pat", &OperatorUser{Login: "alice", Role: RoleOperator})

	if !f.canRead("any/repo") {
		t.Fatal("operator must bypass repo permission check")
	}
	if !f.allowAuditEvent(&AuditEvent{SourceRepo: "any/repo", TargetRepo: "any/other"}) {
		t.Fatal("operator must see all audit events")
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("operator bypass should not call GitHub API, got %d calls", got)
	}
}

func TestRepoFilter_WriterFiltersByPermission(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, map[string]string{
		"org/visible": "read",
		// "org/hidden" not present → 404 → denied
	})
	defer cleanup()
	cache := newGHAuthCache(5 * time.Minute)
	f := newRepoFilter(context.Background(), cache, "pat", &OperatorUser{Login: "writer", Role: RoleWriter})

	if !f.canRead("org/visible") {
		t.Errorf("writer should see org/visible")
	}
	if f.canRead("org/hidden") {
		t.Errorf("writer should not see org/hidden")
	}
	// Empty repo passes (so partial-data rows are scoped by their populated peer).
	if !f.canRead("") {
		t.Errorf("empty repo should pass canRead so partial rows fall through to allowAuditEvent")
	}
}

func TestRepoFilter_AuditEventScopingRules(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, map[string]string{
		"org/visible": "read",
	})
	defer cleanup()
	cache := newGHAuthCache(5 * time.Minute)
	f := newRepoFilter(context.Background(), cache, "pat", &OperatorUser{Login: "w", Role: RoleWriter})

	cases := []struct {
		name string
		ev   AuditEvent
		want bool
	}{
		{"both visible", AuditEvent{SourceRepo: "org/visible", TargetRepo: "org/visible"}, true},
		{"source hidden", AuditEvent{SourceRepo: "org/hidden", TargetRepo: "org/visible"}, false},
		{"target hidden", AuditEvent{SourceRepo: "org/visible", TargetRepo: "org/hidden"}, false},
		{"only source visible (target empty)", AuditEvent{SourceRepo: "org/visible"}, true},
		{"only target visible (source empty)", AuditEvent{TargetRepo: "org/visible"}, true},
		{"both empty (config_change)", AuditEvent{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := f.allowAuditEvent(&tc.ev); got != tc.want {
				t.Errorf("allowAuditEvent(%+v) = %v, want %v", tc.ev, got, tc.want)
			}
		})
	}
}

func TestRepoFilter_MemoisesPerRepo(t *testing.T) {
	cleanup, calls := stubGitHubPerRepo(t, map[string]string{"org/r": "read"})
	defer cleanup()
	cache := newGHAuthCache(5 * time.Minute)
	f := newRepoFilter(context.Background(), cache, "pat", &OperatorUser{Login: "w", Role: RoleWriter})

	for i := 0; i < 5; i++ {
		_ = f.canRead("org/r")
	}
	// First call hits GitHub; the rest are memoised in the filter (no extra hits).
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("want 1 GitHub API call across 5 lookups, got %d", got)
	}
}

// ── handleAuditEvents end-to-end ──

func newAuditTestUI(t *testing.T, events []AuditEvent) *operatorUI {
	t.Helper()
	return &operatorUI{
		cfg: &configs.Config{},
		container: &ServiceContainer{
			AuditLogger: &fakeAuditLogger{events: events},
		},
		ghCache: newGHAuthCache(5 * time.Minute),
	}
}

func TestHandleAuditEvents_WriterSeesOnlyVisibleRepos(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, map[string]string{
		"org/visible-src": "read",
		"org/visible-tgt": "read",
	})
	defer cleanup()

	events := []AuditEvent{
		{SourceRepo: "org/visible-src", TargetRepo: "org/visible-tgt", SourcePath: "a"},
		{SourceRepo: "org/secret", TargetRepo: "org/visible-tgt", SourcePath: "b"},
		{SourceRepo: "org/visible-src", TargetRepo: "org/secret", SourcePath: "c"},
		// config_change: writers don't see operator actions.
		{Actor: "admin", AdditionalData: map[string]any{"setting": "llm"}},
	}
	o := newAuditTestUI(t, events)

	w := httptest.NewRecorder()
	o.handleAuditEvents(w, requestAs(RoleWriter, "writer", "/operator/api/audit/events"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Events []AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Fatalf("writer should see 1 event (both repos visible), got %d: %+v", len(resp.Events), resp.Events)
	}
	if resp.Events[0].SourcePath != "a" {
		t.Errorf("wrong event surfaced: %+v", resp.Events[0])
	}
}

func TestHandleAuditEvents_OperatorSeesEverything(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, nil) // no repos allowed
	defer cleanup()

	events := []AuditEvent{
		{SourceRepo: "org/a", TargetRepo: "org/b"},
		{SourceRepo: "org/c", TargetRepo: "org/d"},
		{Actor: "admin", AdditionalData: map[string]any{"setting": "llm"}},
	}
	o := newAuditTestUI(t, events)

	w := httptest.NewRecorder()
	o.handleAuditEvents(w, requestAs(RoleOperator, "admin", "/operator/api/audit/events"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Events []AuditEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Events) != 3 {
		t.Errorf("operator should see all 3 events, got %d", len(resp.Events))
	}
}

// ── handleObservabilityWebhookTraces ──

func TestHandleObservabilityWebhookTraces_WriterFilteredByRepo(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, map[string]string{"org/visible": "read"})
	defer cleanup()

	tb := NewWebhookTraceBuffer()
	tb.Append(WebhookTraceEntry{DeliveryID: "1", Repo: "org/visible", Detail: "x"})
	tb.Append(WebhookTraceEntry{DeliveryID: "2", Repo: "org/secret", Detail: "y"})
	tb.Append(WebhookTraceEntry{DeliveryID: "3", Detail: "no repo"}) // dropped for writers

	o := &operatorUI{
		cfg:       &configs.Config{},
		container: &ServiceContainer{WebhookTraces: tb},
		ghCache:   newGHAuthCache(5 * time.Minute),
	}

	w := httptest.NewRecorder()
	o.handleObservabilityWebhookTraces(w, requestAs(RoleWriter, "writer", "/operator/api/observability/webhook-traces"))

	var resp struct {
		Traces []WebhookTraceEntry `json:"traces"`
		Total  int                 `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, w.Body.String())
	}
	if len(resp.Traces) != 1 {
		t.Fatalf("writer should see 1 trace, got %d: %+v", len(resp.Traces), resp.Traces)
	}
	if resp.Traces[0].Repo != "org/visible" {
		t.Errorf("wrong trace surfaced: %+v", resp.Traces[0])
	}
	if resp.Total != 3 {
		t.Errorf("total should reflect unfiltered buffer size = 3, got %d", resp.Total)
	}
}

// ── handleDeliveryLogs ──

func TestHandleDeliveryLogs_WriterDeniedWhenRepoUnknown(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, nil)
	defer cleanup()

	o := &operatorUI{
		cfg: &configs.Config{},
		container: &ServiceContainer{
			DeliveryLogs:  NewDeliveryLogBuffer(),
			WebhookTraces: NewWebhookTraceBuffer(), // empty: no trace → no repo
		},
		ghCache: newGHAuthCache(5 * time.Minute),
	}
	o.container.DeliveryLogs.Append("d1", LogEntry{Level: "info", Message: "hi"})

	w := httptest.NewRecorder()
	o.handleDeliveryLogs(w, requestAs(RoleWriter, "writer", "/operator/api/logs?delivery_id=d1"))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (no trace = no repo to authorise)", w.Code)
	}
}

func TestHandleDeliveryLogs_WriterAllowedWhenRepoVisible(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, map[string]string{"org/visible": "read"})
	defer cleanup()

	traces := NewWebhookTraceBuffer()
	traces.Append(WebhookTraceEntry{DeliveryID: "d1", Repo: "org/visible"})

	logs := NewDeliveryLogBuffer()
	logs.Append("d1", LogEntry{Level: "info", Message: "hello"})

	o := &operatorUI{
		cfg: &configs.Config{},
		container: &ServiceContainer{
			DeliveryLogs:  logs,
			WebhookTraces: traces,
		},
		ghCache: newGHAuthCache(5 * time.Minute),
	}

	w := httptest.NewRecorder()
	o.handleDeliveryLogs(w, requestAs(RoleWriter, "writer", "/operator/api/logs?delivery_id=d1"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestHandleDeliveryLogs_WriterDeniedWhenRepoHidden(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, nil) // no repos visible
	defer cleanup()

	traces := NewWebhookTraceBuffer()
	traces.Append(WebhookTraceEntry{DeliveryID: "d1", Repo: "org/secret"})

	logs := NewDeliveryLogBuffer()
	logs.Append("d1", LogEntry{Level: "info", Message: "hello"})

	o := &operatorUI{
		cfg: &configs.Config{},
		container: &ServiceContainer{
			DeliveryLogs:  logs,
			WebhookTraces: traces,
		},
		ghCache: newGHAuthCache(5 * time.Minute),
	}

	w := httptest.NewRecorder()
	o.handleDeliveryLogs(w, requestAs(RoleWriter, "writer", "/operator/api/logs?delivery_id=d1"))

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}

func TestHandleDeliveryLogs_OperatorBypassesScoping(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, nil)
	defer cleanup()

	logs := NewDeliveryLogBuffer()
	logs.Append("d1", LogEntry{Level: "info", Message: "hello"})

	o := &operatorUI{
		cfg: &configs.Config{},
		container: &ServiceContainer{
			DeliveryLogs:  logs,
			WebhookTraces: NewWebhookTraceBuffer(), // intentionally empty
		},
		ghCache: newGHAuthCache(5 * time.Minute),
	}

	w := httptest.NewRecorder()
	o.handleDeliveryLogs(w, requestAs(RoleOperator, "admin", "/operator/api/logs?delivery_id=d1"))

	if w.Code != http.StatusOK {
		t.Fatalf("operator must read logs even without a trace; status=%d body=%s", w.Code, w.Body.String())
	}
}

// ── handleWorkflows ──

// stubConfigLoader is a ConfigLoader that returns a fixed RootConfig.
type stubConfigLoader struct {
	cfg *types.YAMLConfig
}

func (s *stubConfigLoader) LoadConfig(_ context.Context, _ *configs.Config) (*types.YAMLConfig, error) {
	return s.cfg, nil
}

func (s *stubConfigLoader) LoadConfigFromContent(_, _ string) (*types.YAMLConfig, error) {
	return s.cfg, nil
}

func TestHandleWorkflows_WriterFilteredByRepo(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, map[string]string{
		"org/src":      "read",
		"org/dst":      "read",
		"org/half-src": "read",
		// org/half-dst missing → hidden
	})
	defer cleanup()

	root := &types.YAMLConfig{
		Workflows: []types.Workflow{
			{Name: "fully-visible", Source: types.Source{Repo: "org/src"}, Destination: types.Destination{Repo: "org/dst"}},
			{Name: "dest-hidden", Source: types.Source{Repo: "org/half-src"}, Destination: types.Destination{Repo: "org/half-dst"}},
			{Name: "src-hidden", Source: types.Source{Repo: "org/secret"}, Destination: types.Destination{Repo: "org/dst"}},
		},
	}

	o := &operatorUI{
		cfg: &configs.Config{ConfigRepoOwner: "org", ConfigRepoName: "config"},
		container: &ServiceContainer{
			ConfigLoader: &stubConfigLoader{cfg: root},
		},
		ghCache: newGHAuthCache(5 * time.Minute),
	}

	w := httptest.NewRecorder()
	o.handleWorkflows(w, requestAs(RoleWriter, "writer", "/operator/api/workflows"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Workflows   []types.Workflow `json:"workflows"`
		HiddenCount int              `json:"hidden_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Workflows) != 1 || resp.Workflows[0].Name != "fully-visible" {
		t.Fatalf("expected only fully-visible workflow, got %+v", resp.Workflows)
	}
	if resp.HiddenCount != 2 {
		t.Errorf("hidden_count = %d, want 2", resp.HiddenCount)
	}
}

func TestHandleWorkflows_OperatorSeesAll(t *testing.T) {
	cleanup, _ := stubGitHubPerRepo(t, nil)
	defer cleanup()

	root := &types.YAMLConfig{
		Workflows: []types.Workflow{
			{Name: "a", Source: types.Source{Repo: "org/a"}, Destination: types.Destination{Repo: "org/b"}},
			{Name: "b", Source: types.Source{Repo: "org/c"}, Destination: types.Destination{Repo: "org/d"}},
		},
	}

	o := &operatorUI{
		cfg: &configs.Config{},
		container: &ServiceContainer{
			ConfigLoader: &stubConfigLoader{cfg: root},
		},
		ghCache: newGHAuthCache(5 * time.Minute),
	}

	w := httptest.NewRecorder()
	o.handleWorkflows(w, requestAs(RoleOperator, "admin", "/operator/api/workflows"))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Workflows   []types.Workflow `json:"workflows"`
		HiddenCount int              `json:"hidden_count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Workflows) != 2 {
		t.Errorf("operator should see both workflows, got %d", len(resp.Workflows))
	}
	if resp.HiddenCount != 0 {
		t.Errorf("hidden_count must not be set for operator, got %d", resp.HiddenCount)
	}
}
