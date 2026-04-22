package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// anthropicAPIVersion is pinned to the stable Messages API version. Bump this
// only when we intentionally adopt a new API contract.
const anthropicAPIVersion = "2023-06-01"

// defaultAnthropicBaseURL is the hosted Anthropic API. Override (via SetBaseURL
// or LLM_BASE_URL) only to route through a gateway or proxy that speaks the
// same wire format.
const defaultAnthropicBaseURL = "https://api.anthropic.com"

// anthropicFallbackModels is used when /v1/models returns an empty list or
// errors (e.g. behind a gateway that doesn't expose it). Kept deliberately
// minimal — listing every model here means every rotation ships dead
// dropdown options. Aliased names route to the current dated release, so
// this single entry stays valid across point releases.
var anthropicFallbackModels = []LLMModel{
	{Name: "claude-haiku-4-5"},
}

type anthropicClient struct {
	mu      sync.RWMutex
	baseURL string
	model   string
	apiKey  string
	http    *http.Client
}

func newAnthropicClient(baseURL, model, apiKey string) *anthropicClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultAnthropicBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = "claude-haiku-4-5"
	}
	return &anthropicClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		model:   model,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *anthropicClient) ProviderName() string { return "anthropic" }

func (c *anthropicClient) GetBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

func (c *anthropicClient) SetBaseURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimSuffix(strings.TrimSpace(url), "/")
}

func (c *anthropicClient) GetActiveModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

func (c *anthropicClient) SetActiveModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = strings.TrimSpace(model)
}

// newAuthedRequest builds a request with the Anthropic auth + version headers.
// Callers must have already validated that URL components are not user-supplied;
// the base URL is derived from a pinned default or operator-set value.
//
// We set both x-api-key (native Anthropic) and api-key (Azure API Management
// gateway convention) so the same client works when LLM_BASE_URL points at
// either the direct API or an APIM-fronted proxy. Sending both is harmless —
// the target service uses whichever it recognizes and ignores the other.
func (c *anthropicClient) newAuthedRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.GetBaseURL()+path, body) // #nosec G107 -- base URL is pinned default or operator-set; path is a literal constant
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("api-key", c.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	req.Header.Set("content-type", "application/json")
	return req, nil
}

// Ping issues a minimal /v1/messages call as an auth + reachability check.
// Using /v1/messages (rather than /v1/models) keeps this working behind
// proxies — including Azure APIM-fronted gateways — that only expose the
// messages endpoint. Cost per ping is roughly 1 input + 1 output token.
func (c *anthropicClient) Ping(ctx context.Context) error {
	if strings.TrimSpace(c.apiKey) == "" {
		return fmt.Errorf("ANTHROPIC_API_KEY is not configured")
	}
	body, _ := json.Marshal(anthropicMessagesRequest{
		Model:     c.GetActiveModel(),
		MaxTokens: 1,
		Messages:  []anthropicMessage{{Role: "user", Content: "ping"}},
	})
	req, err := c.newAuthedRequest(ctx, http.MethodPost, "/v1/messages", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req) // #nosec G107 -- see newAuthedRequest
	if err != nil {
		return fmt.Errorf("anthropic unreachable at %s: %w", c.GetBaseURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("anthropic auth failed (HTTP %d) — check ANTHROPIC_API_KEY", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<15))
		return fmt.Errorf("anthropic returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

type anthropicModelsResponse struct {
	Data []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
	} `json:"data"`
}

// ListModels returns models available to the account. Falls back to a static
// list if the API call fails so the UI stays usable.
func (c *anthropicClient) ListModels(ctx context.Context) ([]LLMModel, error) {
	req, err := c.newAuthedRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req) // #nosec G107 -- see newAuthedRequest
	if err != nil {
		return anthropicFallbackModels, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return anthropicFallbackModels, nil
	}
	var out anthropicModelsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return anthropicFallbackModels, nil
	}
	if len(out.Data) == 0 {
		return anthropicFallbackModels, nil
	}
	models := make([]LLMModel, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, LLMModel{Name: m.ID, ModifiedAt: m.CreatedAt})
	}
	return models, nil
}

// PullModel / DeleteModel are not supported for hosted providers. The UI
// hides the relevant sections when provider != "ollama", so these should not
// normally be reached; returning a sentinel lets the HTTP layer map cleanly.
func (c *anthropicClient) PullModel(_ context.Context, _ string, _ func(LLMPullProgress)) error {
	return ErrModelManagementNotSupported
}

func (c *anthropicClient) DeleteModel(_ context.Context, _ string) error {
	return ErrModelManagementNotSupported
}

// anthropicMessagesRequest is the body of POST /v1/messages.
type anthropicMessagesRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicMessagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// jsonGuardrail is appended to the system prompt to nudge the model toward
// raw JSON output. Anthropic has no native JSON mode on /v1/messages, so we
// rely on prompting + a post-processing fence strip.
const jsonGuardrail = "\n\nRespond with ONLY valid JSON — no prose, no explanations outside the JSON, no code fences, no backticks. Just the JSON object."

func (c *anthropicClient) GenerateJSON(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if strings.TrimSpace(c.apiKey) == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY is not configured")
	}
	reqBody, err := json.Marshal(anthropicMessagesRequest{
		Model:     c.GetActiveModel(),
		MaxTokens: 4096,
		System:    systemPrompt + jsonGuardrail,
		Messages:  []anthropicMessage{{Role: "user", Content: userPrompt}},
	})
	if err != nil {
		return "", fmt.Errorf("marshal anthropic request: %w", err)
	}

	req, err := c.newAuthedRequest(ctx, http.MethodPost, "/v1/messages", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req) // #nosec G107 -- see newAuthedRequest
	if err != nil {
		return "", fmt.Errorf("call anthropic at %s: %w", c.GetBaseURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var out anthropicMessagesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse anthropic response: %w", err)
	}
	if out.Error != nil {
		return "", fmt.Errorf("anthropic error: %s: %s", out.Error.Type, out.Error.Message)
	}
	// Concatenate all text blocks (usually one).
	var sb strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}
	raw := strings.TrimSpace(sb.String())
	if raw == "" {
		return "", fmt.Errorf("anthropic returned empty response (model %q)", c.GetActiveModel())
	}
	return stripJSONFences(raw), nil
}

// stripJSONFences removes ```json ... ``` or ``` ... ``` wrappers that models
// sometimes add despite being asked for raw JSON. If the input doesn't look
// fenced, it's returned unchanged.
func stripJSONFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	// Drop the opening fence (```json or ```)
	if nl := strings.IndexByte(t, '\n'); nl >= 0 {
		t = t[nl+1:]
	} else {
		return strings.TrimSpace(strings.TrimPrefix(t, "```"))
	}
	// Drop the trailing fence
	if idx := strings.LastIndex(t, "```"); idx >= 0 {
		t = t[:idx]
	}
	return strings.TrimSpace(t)
}
