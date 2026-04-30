package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grove-platform/github-copier/configs"
)

// ── renderRuleYAML golden tests ──

func TestRenderRuleYAML_Move(t *testing.T) {
	got := renderRuleYAML(&llmSuggestedRule{
		Name:           "mflix-java-spring-server",
		DestRepo:       "mongodb/sample-app-java-mflix",
		DestBranch:     "main",
		TransformType:  "move",
		TransformFrom:  "mflix/server/java-spring",
		TransformTo:    "server",
		CommitStrategy: "pull_request",
	}, operatorSuggestRuleRequest{
		SourceRepo: "mongodb/docs-content",
	})
	want := `- name: "mflix-java-spring-server"
  source:
    repo: "mongodb/docs-content"
  destination:
    repo: "mongodb/sample-app-java-mflix"
    branch: "main"
  transformations:
    - move: { from: "mflix/server/java-spring", to: "server" }
  commit_strategy:
    type: "pull_request"
`
	if got != want {
		t.Errorf("move YAML mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestRenderRuleYAML_Copy(t *testing.T) {
	got := renderRuleYAML(&llmSuggestedRule{
		Name:           "mflix-readme",
		DestRepo:       "mongodb/sample-app-java-mflix",
		DestBranch:     "main",
		TransformType:  "copy",
		TransformFrom:  "mflix/README-JAVA-SPRING.md",
		TransformTo:    "README.md",
		CommitStrategy: "pull_request",
	}, operatorSuggestRuleRequest{SourceRepo: "mongodb/docs-content"})
	want := `- name: "mflix-readme"
  source:
    repo: "mongodb/docs-content"
  destination:
    repo: "mongodb/sample-app-java-mflix"
    branch: "main"
  transformations:
    - copy: { from: "mflix/README-JAVA-SPRING.md", to: "README.md" }
  commit_strategy:
    type: "pull_request"
`
	if got != want {
		t.Errorf("copy YAML mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestRenderRuleYAML_Glob(t *testing.T) {
	got := renderRuleYAML(&llmSuggestedRule{
		Name:           "agg-python",
		DestRepo:       "org/shared-examples",
		DestBranch:     "main",
		TransformType:  "glob",
		Pattern:        "agg/python/**/*.py",
		TransformTempl: "shared/python/${relative_path}",
		CommitStrategy: "direct",
	}, operatorSuggestRuleRequest{SourceRepo: "org/aggregations"})
	want := `- name: "agg-python"
  source:
    repo: "org/aggregations"
  destination:
    repo: "org/shared-examples"
    branch: "main"
  transformations:
    - glob:
        pattern: "agg/python/**/*.py"
        transform: "shared/python/${relative_path}"
  commit_strategy:
    type: "direct"
`
	if got != want {
		t.Errorf("glob YAML mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestRenderRuleYAML_Regex(t *testing.T) {
	got := renderRuleYAML(&llmSuggestedRule{
		Name:           "tutorials-versioned",
		DestRepo:       "org/docs-site",
		DestBranch:     "main",
		TransformType:  "regex",
		Pattern:        `tutorials/v(?P<ver>[0-9]+)/(?P<slug>.+)\.mdx`,
		TransformTempl: "docs/${slug}-v${ver}.mdx",
		CommitStrategy: "pull_request",
	}, operatorSuggestRuleRequest{SourceRepo: "org/tutorials"})
	want := `- name: "tutorials-versioned"
  source:
    repo: "org/tutorials"
  destination:
    repo: "org/docs-site"
    branch: "main"
  transformations:
    - regex:
        pattern: "tutorials/v(?P<ver>[0-9]+)/(?P<slug>.+)\\.mdx"
        transform: "docs/${slug}-v${ver}.mdx"
  commit_strategy:
    type: "pull_request"
`
	if got != want {
		t.Errorf("regex YAML mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

// When the operator UI didn't send a source_repo, the YAML must omit the
// entire source: block — the writer fills it in by hand.
func TestRenderRuleYAML_OmitsSourceBlockWhenSourceRepoEmpty(t *testing.T) {
	got := renderRuleYAML(&llmSuggestedRule{
		Name:           "x",
		DestRepo:       "org/dst",
		DestBranch:     "main",
		TransformType:  "move",
		TransformFrom:  "a",
		TransformTo:    "b",
		CommitStrategy: "pull_request",
	}, operatorSuggestRuleRequest{}) // SourceRepo intentionally empty
	if strings.Contains(got, "source:") {
		t.Errorf("source: block should be omitted when SourceRepo is empty\nyaml:\n%s", got)
	}
	if !strings.Contains(got, "destination:") {
		t.Errorf("destination: block must always be present\nyaml:\n%s", got)
	}
}

// ── verifySuggestedRule: move-transform path-boundary test ──
//
// Reviewer flagged that strings.HasPrefix without a "/" boundary check would
// pass verification for from="agg/py" against source="agg/python/foo.py".
// This test pins the corrected behaviour: such mismatches must reject.

func TestVerifySuggestedRule_Move_RejectsNonBoundaryPrefix(t *testing.T) {
	s := &llmSuggestedRule{
		TransformType: "move",
		TransformFrom: "agg/py",
		TransformTo:   "shared/py",
	}
	ok, _, err := verifySuggestedRule(s, "agg/python/models/user.py", "shared/py/thon/models/user.py")
	if ok {
		t.Fatal("verifier must not accept from=agg/py for source=agg/python/...; that's a substring match, not a path-component match")
	}
	if err == nil {
		t.Fatal("want a boundary-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "boundary") {
		t.Errorf("error should mention boundary, got %q", err.Error())
	}
}

func TestVerifySuggestedRule_Move_AcceptsExactMatchAtBoundary(t *testing.T) {
	// Source equals from exactly (single-file directory rename).
	s := &llmSuggestedRule{
		TransformType: "move",
		TransformFrom: "agg/python",
		TransformTo:   "shared/python",
	}
	ok, computed, err := verifySuggestedRule(s, "agg/python", "shared/python")
	if err != nil || !ok {
		t.Fatalf("exact-match boundary case failed: ok=%v computed=%q err=%v", ok, computed, err)
	}
}

// ── handleSuggestRule branch coverage ──

// fakeLLMClient records prompts and returns canned responses.
type fakeLLMClient struct {
	respJSON string
	err      error
	lastSys  string
	lastUser string
}

func (f *fakeLLMClient) GenerateJSON(_ context.Context, sys, user string) (string, error) {
	f.lastSys = sys
	f.lastUser = user
	return f.respJSON, f.err
}
func (f *fakeLLMClient) ProviderName() string         { return "fake" }
func (f *fakeLLMClient) Ping(_ context.Context) error { return nil }
func (f *fakeLLMClient) GetBaseURL() string           { return "" }
func (f *fakeLLMClient) SetBaseURL(_ string)          {}
func (f *fakeLLMClient) GetActiveModel() string       { return "fake-model" }
func (f *fakeLLMClient) SetActiveModel(_ string)      {}
func (f *fakeLLMClient) ListModels(_ context.Context) ([]LLMModel, error) {
	return nil, nil
}
func (f *fakeLLMClient) PullModel(_ context.Context, _ string, _ func(LLMPullProgress)) error {
	return ErrModelManagementNotSupported
}
func (f *fakeLLMClient) DeleteModel(_ context.Context, _ string) error {
	return ErrModelManagementNotSupported
}

func newSuggestRuleTestUI(llm LLMClient) *operatorUI {
	return &operatorUI{
		cfg:            &configs.Config{},
		container:      &ServiceContainer{},
		llm:            llm,
		ghCache:        newGHAuthCache(5 * time.Minute),
		suggestLimiter: newTokenBucket(30, time.Hour),
	}
}

func suggestRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/operator/api/suggest-rule", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer pat-test")
	return r
}

func TestHandleSuggestRule_MethodNotAllowed(t *testing.T) {
	o := newSuggestRuleTestUI(&fakeLLMClient{})
	r := httptest.NewRequest(http.MethodGet, "/operator/api/suggest-rule", nil)
	w := httptest.NewRecorder()
	o.handleSuggestRule(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleSuggestRule_LLMNotInitialized(t *testing.T) {
	o := newSuggestRuleTestUI(nil)
	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"a","target_path":"b"}`))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleSuggestRule_RateLimited(t *testing.T) {
	o := newSuggestRuleTestUI(&fakeLLMClient{respJSON: `{}`})
	o.suggestLimiter = newTokenBucket(1, time.Hour)
	// First call consumes the only token.
	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"a","target_path":"b"}`))
	// Second call must be rate limited regardless of body validity.
	w = httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"a","target_path":"b"}`))
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("missing Retry-After header on 429")
	}
}

func TestHandleSuggestRule_InvalidJSON(t *testing.T) {
	o := newSuggestRuleTestUI(&fakeLLMClient{})
	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{not-json`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleSuggestRule_MissingRequiredFields(t *testing.T) {
	o := newSuggestRuleTestUI(&fakeLLMClient{})
	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"only"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleSuggestRule_LLMError(t *testing.T) {
	o := newSuggestRuleTestUI(&fakeLLMClient{err: errors.New("upstream nope")})
	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"a","target_path":"b"}`))
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.Code)
	}
}

func TestHandleSuggestRule_LLMReturnsInvalidJSON(t *testing.T) {
	o := newSuggestRuleTestUI(&fakeLLMClient{respJSON: "not json at all"})
	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"a","target_path":"b"}`))
	if w.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 when LLM emits invalid JSON", w.Code)
	}
}

func TestHandleSuggestRule_VerifiedFalseSetsWarning(t *testing.T) {
	// LLM returns a "move" rule that doesn't actually produce target_path —
	// verification fails, the response should include the warning + verify_error.
	llmResp := `{
		"name":"x",
		"destination_repo":"org/dst",
		"transform_type":"move",
		"transform_from":"a/b",
		"transform_to":"x/y"
	}`
	o := newSuggestRuleTestUI(&fakeLLMClient{respJSON: llmResp})

	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"a/b/file.txt","target_path":"completely/different.txt"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (verification failure is non-fatal)", w.Code)
	}
	var resp operatorSuggestRuleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Verified {
		t.Errorf("verified should be false")
	}
	if resp.Warning == "" {
		t.Errorf("warning should be set when verification fails")
	}
	if resp.ComputedPath == "" {
		t.Errorf("computed_path should be populated even on verification failure")
	}
}

func TestHandleSuggestRule_AppliesDefaultsFromAskLLMForRule(t *testing.T) {
	// LLM omits destination_branch, commit_strategy, name, destination_repo —
	// askLLMForRule fills them in. Run an end-to-end suggest call and inspect
	// the rendered YAML to confirm defaults landed.
	llmResp := `{
		"transform_type":"copy",
		"transform_from":"src/file.txt",
		"transform_to":"dst/file.txt"
	}`
	o := newSuggestRuleTestUI(&fakeLLMClient{respJSON: llmResp})

	w := httptest.NewRecorder()
	o.handleSuggestRule(w, suggestRequest(t, `{"source_path":"src/file.txt","target_path":"dst/file.txt","target_repo":"org/from-request"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp operatorSuggestRuleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// destination_repo defaulted from target_repo in the request.
	if !strings.Contains(resp.RuleYAML, `repo: "org/from-request"`) {
		t.Errorf("dest repo default missing from yaml:\n%s", resp.RuleYAML)
	}
	// destination_branch defaulted to "main".
	if !strings.Contains(resp.RuleYAML, `branch: "main"`) {
		t.Errorf("default branch missing from yaml:\n%s", resp.RuleYAML)
	}
	// commit_strategy defaulted to "pull_request".
	if !strings.Contains(resp.RuleYAML, `type: "pull_request"`) {
		t.Errorf("default commit strategy missing from yaml:\n%s", resp.RuleYAML)
	}
	// name defaulted to "generated-rule".
	if !strings.Contains(resp.RuleYAML, `name: "generated-rule"`) {
		t.Errorf("default name missing from yaml:\n%s", resp.RuleYAML)
	}
}
