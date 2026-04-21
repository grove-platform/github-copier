package services

import (
	"bufio"
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

// LLMClient is the minimal interface used by the operator UI. It supports
// runtime reconfiguration (active model, base URL) and provider management
// operations (list/pull/delete models).
type LLMClient interface {
	// GenerateJSON sends a prompt to the LLM and returns the raw response body.
	GenerateJSON(ctx context.Context, systemPrompt, userPrompt string) (string, error)

	// ProviderName returns a short identifier for logging.
	ProviderName() string

	// Ping checks whether the LLM service is reachable.
	Ping(ctx context.Context) error

	// GetBaseURL returns the current base URL.
	GetBaseURL() string
	// SetBaseURL updates the base URL at runtime.
	SetBaseURL(url string)

	// GetActiveModel returns the model that will be used for generations.
	GetActiveModel() string
	// SetActiveModel updates the active model at runtime.
	SetActiveModel(model string)

	// ListModels returns the models installed/available on the LLM server.
	ListModels(ctx context.Context) ([]LLMModel, error)

	// PullModel asks the server to download a model. Progress updates are
	// written to progress as they arrive. The function blocks until the pull
	// completes or the context is cancelled.
	PullModel(ctx context.Context, name string, progress func(LLMPullProgress)) error

	// DeleteModel removes a model from the server.
	DeleteModel(ctx context.Context, name string) error
}

// LLMModel describes an installed model returned by the provider.
type LLMModel struct {
	Name       string `json:"name"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

// LLMPullProgress is a single progress event emitted during PullModel.
type LLMPullProgress struct {
	Status    string `json:"status"`
	Completed int64  `json:"completed,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Digest    string `json:"digest,omitempty"`
	Error     string `json:"error,omitempty"`
}

// NewLLMClient returns a client for the configured provider.
func NewLLMClient(provider, baseURL, model string) (LLMClient, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "ollama":
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		if model == "" {
			model = "qwen2.5-coder:7b"
		}
		return &ollamaClient{
			baseURL:  strings.TrimSuffix(baseURL, "/"),
			model:    model,
			http:     &http.Client{Timeout: 60 * time.Second},
			pullHTTP: &http.Client{
				// No timeout for pulls — model downloads can take 10+ minutes
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %q (only \"ollama\" is implemented)", provider)
	}
}

// ── Ollama ──

type ollamaClient struct {
	mu       sync.RWMutex
	baseURL  string
	model    string
	http     *http.Client // short-timeout client for most calls
	pullHTTP *http.Client // no-timeout client for streaming pull requests
}

func (c *ollamaClient) ProviderName() string { return "ollama" }

func (c *ollamaClient) GetBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL
}

func (c *ollamaClient) SetBaseURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimSuffix(strings.TrimSpace(url), "/")
}

func (c *ollamaClient) GetActiveModel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.model
}

func (c *ollamaClient) SetActiveModel(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = strings.TrimSpace(model)
}

// Ping calls GET /api/tags as a reachability check (cheap, no model load).
func (c *ollamaClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.GetBaseURL()+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ollama unreachable at %s: %w", c.GetBaseURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<15))
		return fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// ollamaTagsResponse is GET /api/tags.
type ollamaTagsResponse struct {
	Models []struct {
		Name       string `json:"name"`
		Size       int64  `json:"size"`
		ModifiedAt string `json:"modified_at"`
	} `json:"models"`
}

func (c *ollamaClient) ListModels(ctx context.Context) ([]LLMModel, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.GetBaseURL()+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tags ollamaTagsResponse
	if err := json.Unmarshal(body, &tags); err != nil {
		return nil, fmt.Errorf("parse models: %w", err)
	}
	out := make([]LLMModel, 0, len(tags.Models))
	for _, m := range tags.Models {
		out = append(out, LLMModel{Name: m.Name, Size: m.Size, ModifiedAt: m.ModifiedAt})
	}
	return out, nil
}

// PullModel starts a model pull and streams NDJSON progress events.
func (c *ollamaClient) PullModel(ctx context.Context, name string, progress func(LLMPullProgress)) error {
	body, _ := json.Marshal(map[string]any{"name": name, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.GetBaseURL()+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.pullHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("start pull: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<15))
		return fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	// Ollama emits newline-delimited JSON progress events. Stream them through.
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev LLMPullProgress
		if err := json.Unmarshal(line, &ev); err != nil {
			// Skip unparseable lines rather than aborting
			continue
		}
		if progress != nil {
			progress(ev)
		}
		if ev.Error != "" {
			return fmt.Errorf("pull error: %s", ev.Error)
		}
	}
	return scanner.Err()
}

// DeleteModel removes a locally installed model.
func (c *ollamaClient) DeleteModel(ctx context.Context, name string) error {
	body, _ := json.Marshal(map[string]string{"name": name})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.GetBaseURL()+"/api/delete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete model: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<15))
		return fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// ollamaGenerateRequest is the body of POST /api/generate.
type ollamaGenerateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Stream bool   `json:"stream"`
	Format string `json:"format,omitempty"` // "json" constrains output to valid JSON
}

type ollamaGenerateResponse struct {
	Model     string `json:"model"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
	DoneError string `json:"error,omitempty"`
}

func (c *ollamaClient) GenerateJSON(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	body, err := json.Marshal(ollamaGenerateRequest{
		Model:  c.GetActiveModel(),
		System: systemPrompt,
		Prompt: userPrompt,
		Stream: false,
		Format: "json",
	})
	if err != nil {
		return "", fmt.Errorf("marshal ollama request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.GetBaseURL()+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call ollama at %s: %w (is ollama running?)", c.GetBaseURL(), err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var out ollamaGenerateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("parse ollama response: %w", err)
	}
	if out.DoneError != "" {
		return "", fmt.Errorf("ollama error: %s", out.DoneError)
	}
	if out.Response == "" {
		return "", fmt.Errorf("ollama returned empty response (check that model %q is pulled)", c.GetActiveModel())
	}
	return out.Response, nil
}
