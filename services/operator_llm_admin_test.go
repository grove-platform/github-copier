package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/grove-platform/github-copier/configs"
)

func TestValidateLLMBaseURL(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		raw      string
		allowed  []string
		wantErr  string // substring; empty = expect success
		wantURL  string
	}{
		{
			name:     "anthropic accepts https",
			provider: "anthropic",
			raw:      "https://api.anthropic.com",
			wantURL:  "https://api.anthropic.com",
		},
		{
			name:     "anthropic trims trailing slash",
			provider: "anthropic",
			raw:      "https://gateway.example.com/v1/",
			wantURL:  "https://gateway.example.com/v1",
		},
		{
			name:     "anthropic rejects http",
			provider: "anthropic",
			raw:      "http://attacker.example.com",
			wantErr:  "must use https",
		},
		{
			name:     "anthropic rejects userinfo",
			provider: "anthropic",
			raw:      "https://user:pass@victim.example.com",
			wantErr:  "userinfo",
		},
		{
			name:     "anthropic rejects schemeless",
			provider: "anthropic",
			raw:      "api.anthropic.com",
			wantErr:  "absolute URL",
		},
		{
			name:     "anthropic rejects file scheme (no host)",
			provider: "anthropic",
			raw:      "file:///etc/passwd",
			wantErr:  "absolute URL",
		},
		{
			name:     "anthropic rejects empty",
			provider: "anthropic",
			raw:      "   ",
			wantErr:  "required",
		},
		{
			name:     "ollama accepts http localhost",
			provider: "ollama",
			raw:      "http://localhost:11434",
			wantURL:  "http://localhost:11434",
		},
		{
			name:     "ollama accepts https",
			provider: "ollama",
			raw:      "https://ollama.internal",
			wantURL:  "https://ollama.internal",
		},
		{
			name:     "ollama rejects ftp",
			provider: "ollama",
			raw:      "ftp://example.com",
			wantErr:  "http or https",
		},
		{
			name:     "empty provider treated as ollama (allows http)",
			provider: "",
			raw:      "http://localhost:11434",
			wantURL:  "http://localhost:11434",
		},
		{
			name:     "unknown provider requires https",
			provider: "openai",
			raw:      "http://example.com",
			wantErr:  "must use https",
		},
		{
			name:     "allowlist accepts matching host",
			provider: "anthropic",
			raw:      "https://api.anthropic.com",
			allowed:  []string{"api.anthropic.com", "anthropic.gateway.example.com"},
			wantURL:  "https://api.anthropic.com",
		},
		{
			name:     "allowlist rejects non-matching host",
			provider: "anthropic",
			raw:      "https://attacker.example.com",
			allowed:  []string{"api.anthropic.com"},
			wantErr:  "not in the allowlist",
		},
		{
			name:     "allowlist matches case-insensitively",
			provider: "anthropic",
			raw:      "https://API.Anthropic.com",
			allowed:  []string{"api.anthropic.com"},
			wantURL:  "https://API.Anthropic.com",
		},
		{
			name:     "allowlist with port matches when configured",
			provider: "anthropic",
			raw:      "https://gateway.example.com:8443",
			allowed:  []string{"gateway.example.com:8443"},
			wantURL:  "https://gateway.example.com:8443",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateLLMBaseURL(tc.provider, tc.raw, tc.allowed)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil (result=%q)", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %q", tc.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantURL {
				t.Errorf("got %q, want %q", got, tc.wantURL)
			}
		})
	}
}

// recordingAuditLogger captures LogConfigChangeEvent calls for assertions.
// It embeds NoOpAuditLogger so we don't have to implement the full interface.
type recordingAuditLogger struct {
	NoOpAuditLogger
	mu     sync.Mutex
	events []AuditEvent
}

func (r *recordingAuditLogger) LogConfigChangeEvent(_ context.Context, ev *AuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Copy so test assertions aren't racing with other writes.
	r.events = append(r.events, *ev)
	return nil
}

// stubLLMClient is a minimal LLMClient for handler tests.
type stubLLMClient struct {
	mu      sync.Mutex
	baseURL string
	model   string
}

func (s *stubLLMClient) GenerateJSON(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (s *stubLLMClient) ProviderName() string         { return "anthropic" }
func (s *stubLLMClient) Ping(_ context.Context) error { return nil }
func (s *stubLLMClient) GetBaseURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseURL
}
func (s *stubLLMClient) SetBaseURL(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.baseURL = strings.TrimSuffix(strings.TrimSpace(u), "/")
}
func (s *stubLLMClient) GetActiveModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}
func (s *stubLLMClient) SetActiveModel(m string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = strings.TrimSpace(m)
}
func (s *stubLLMClient) ListModels(_ context.Context) ([]LLMModel, error) { return nil, nil }
func (s *stubLLMClient) PullModel(_ context.Context, _ string, _ func(LLMPullProgress)) error {
	return ErrModelManagementNotSupported
}
func (s *stubLLMClient) DeleteModel(_ context.Context, _ string) error {
	return ErrModelManagementNotSupported
}

func newLLMSettingsTestHarness(t *testing.T, provider string) (*operatorUI, *stubLLMClient, *recordingAuditLogger) {
	t.Helper()
	llm := &stubLLMClient{
		baseURL: "https://api.anthropic.com",
		model:   "claude-haiku-4-5",
	}
	audit := &recordingAuditLogger{}
	o := &operatorUI{
		cfg: &configs.Config{LLMProvider: provider},
		container: &ServiceContainer{
			AuditLogger: audit,
		},
		llm: llm,
	}
	return o, llm, audit
}

// requestWithOperator builds a POST request whose context carries an
// authenticated operator user — handleLLMSettings reads this for the audit
// actor field.
func requestWithOperator(login, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/operator/api/llm/settings", strings.NewReader(body))
	ctx := context.WithValue(r.Context(), operatorUserCtxKey{}, &OperatorUser{Login: login, Role: RoleOperator})
	return r.WithContext(ctx)
}

func TestHandleLLMSettings_RejectsHTTPForAnthropic(t *testing.T) {
	o, llm, audit := newLLMSettingsTestHarness(t, "anthropic")
	r := requestWithOperator("alice", `{"base_url":"http://attacker.example.com"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if got := llm.GetBaseURL(); got != "https://api.anthropic.com" {
		t.Errorf("base URL must not change on validation failure, got %q", got)
	}
	if len(audit.events) != 0 {
		t.Errorf("must not audit a rejected change, got %d events", len(audit.events))
	}
}

func TestHandleLLMSettings_RejectsUserinfoForAnthropic(t *testing.T) {
	o, llm, _ := newLLMSettingsTestHarness(t, "anthropic")
	r := requestWithOperator("alice", `{"base_url":"https://attacker@victim.example.com"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", w.Code, w.Body.String())
	}
	if got := llm.GetBaseURL(); got != "https://api.anthropic.com" {
		t.Errorf("base URL must not change, got %q", got)
	}
}

func TestHandleLLMSettings_AcceptsHTTPForOllama(t *testing.T) {
	o, llm, audit := newLLMSettingsTestHarness(t, "ollama")
	llm.baseURL = "http://localhost:11434"
	r := requestWithOperator("bob", `{"base_url":"http://localhost:9999"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if got := llm.GetBaseURL(); got != "http://localhost:9999" {
		t.Errorf("base URL not applied, got %q", got)
	}
	if len(audit.events) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(audit.events))
	}
}

func TestHandleLLMSettings_AuditsBaseURLChange(t *testing.T) {
	o, _, audit := newLLMSettingsTestHarness(t, "anthropic")
	r := requestWithOperator("alice", `{"base_url":"https://gateway.example.com/v1"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if len(audit.events) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(audit.events))
	}
	ev := audit.events[0]
	if ev.Actor != "alice" {
		t.Errorf("actor = %q, want alice", ev.Actor)
	}
	if !ev.Success {
		t.Errorf("success should be true for an applied change")
	}
	if got := ev.AdditionalData["old_base_url"]; got != "https://api.anthropic.com" {
		t.Errorf("old_base_url = %v, want https://api.anthropic.com", got)
	}
	if got := ev.AdditionalData["new_base_url"]; got != "https://gateway.example.com/v1" {
		t.Errorf("new_base_url = %v, want https://gateway.example.com/v1", got)
	}
	if got := ev.AdditionalData["provider"]; got != "anthropic" {
		t.Errorf("provider = %v, want anthropic", got)
	}
}

func TestHandleLLMSettings_NoChangeNoAudit(t *testing.T) {
	o, _, audit := newLLMSettingsTestHarness(t, "anthropic")
	// Submit the same base URL the client already has.
	r := requestWithOperator("alice", `{"base_url":"https://api.anthropic.com"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(audit.events) != 0 {
		t.Errorf("must not audit a no-op submission, got %d events", len(audit.events))
	}
}

func TestHandleLLMSettings_AuditsModelChange(t *testing.T) {
	o, _, audit := newLLMSettingsTestHarness(t, "anthropic")
	r := requestWithOperator("alice", `{"active_model":"claude-sonnet-4-6"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if len(audit.events) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(audit.events))
	}
	ev := audit.events[0]
	if got := ev.AdditionalData["old_model"]; got != "claude-haiku-4-5" {
		t.Errorf("old_model = %v", got)
	}
	if got := ev.AdditionalData["new_model"]; got != "claude-sonnet-4-6" {
		t.Errorf("new_model = %v", got)
	}
}

func TestHandleLLMSettings_EnforcesAllowlist(t *testing.T) {
	t.Setenv(llmBaseURLAllowedHostsEnv, "api.anthropic.com, gateway.example.com")
	o, llm, audit := newLLMSettingsTestHarness(t, "anthropic")

	// Disallowed host
	r := requestWithOperator("alice", `{"base_url":"https://other.example.com"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if len(audit.events) != 0 {
		t.Fatalf("must not audit a rejected change, got %d events", len(audit.events))
	}

	// Allowlisted host succeeds
	r = requestWithOperator("alice", `{"base_url":"https://gateway.example.com"}`)
	w = httptest.NewRecorder()
	o.handleLLMSettings(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("allowlisted host rejected: status %d, body %s", w.Code, w.Body.String())
	}
	if got := llm.GetBaseURL(); got != "https://gateway.example.com" {
		t.Errorf("base URL not applied, got %q", got)
	}
}

func TestHandleLLMSettings_InvalidJSON(t *testing.T) {
	o, llm, _ := newLLMSettingsTestHarness(t, "anthropic")
	r := requestWithOperator("alice", `{not-json`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := llm.GetBaseURL(); got != "https://api.anthropic.com" {
		t.Errorf("base URL must not change on invalid input, got %q", got)
	}
}

func TestHandleLLMSettings_DecodeResponseBody(t *testing.T) {
	o, _, _ := newLLMSettingsTestHarness(t, "anthropic")
	r := requestWithOperator("alice", `{"base_url":"https://api.anthropic.com/v2/"}`)
	w := httptest.NewRecorder()
	o.handleLLMSettings(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if got := body["base_url"]; got != "https://api.anthropic.com/v2" {
		t.Errorf("base_url in response = %v, want trimmed value", got)
	}
}
