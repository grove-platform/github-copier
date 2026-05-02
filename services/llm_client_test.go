package services

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewLLMClient_Dispatch(t *testing.T) {
	cases := []struct {
		name         string
		opts         LLMClientOptions
		wantProvider string
		wantErr      bool
		errSubstring string
	}{
		{
			name:         "empty provider defaults to ollama",
			opts:         LLMClientOptions{},
			wantProvider: "ollama",
		},
		{
			name:         "explicit ollama",
			opts:         LLMClientOptions{Provider: "ollama"},
			wantProvider: "ollama",
		},
		{
			name:         "case-insensitive provider name",
			opts:         LLMClientOptions{Provider: "Ollama"},
			wantProvider: "ollama",
		},
		{
			name:         "anthropic requires API key",
			opts:         LLMClientOptions{Provider: "anthropic"},
			wantErr:      true,
			errSubstring: "ANTHROPIC_API_KEY",
		},
		{
			name:         "anthropic with key succeeds",
			opts:         LLMClientOptions{Provider: "anthropic", APIKey: "sk-test"},
			wantProvider: "anthropic",
		},
		{
			name:         "unsupported provider errors",
			opts:         LLMClientOptions{Provider: "openai"},
			wantErr:      true,
			errSubstring: "unsupported",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewLLMClient(tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if tc.errSubstring != "" && !strings.Contains(err.Error(), tc.errSubstring) {
					t.Errorf("want error containing %q, got %q", tc.errSubstring, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if client == nil {
				t.Fatalf("want non-nil client")
			}
			if client.ProviderName() != tc.wantProvider {
				t.Errorf("ProviderName()=%q, want %q", client.ProviderName(), tc.wantProvider)
			}
		})
	}
}

func TestAnthropicClient_ModelManagementNotSupported(t *testing.T) {
	c := newAnthropicClient("", "", "sk-test")
	ctx := context.Background()
	if err := c.PullModel(ctx, "anything", nil); !errors.Is(err, ErrModelManagementNotSupported) {
		t.Errorf("PullModel: want ErrModelManagementNotSupported, got %v", err)
	}
	if err := c.DeleteModel(ctx, "anything"); !errors.Is(err, ErrModelManagementNotSupported) {
		t.Errorf("DeleteModel: want ErrModelManagementNotSupported, got %v", err)
	}
}

func TestAnthropicClient_SetGetters(t *testing.T) {
	c := newAnthropicClient("", "", "sk-test")
	if c.GetBaseURL() != "https://api.anthropic.com" {
		t.Errorf("default base URL mismatch: %q", c.GetBaseURL())
	}
	if c.GetActiveModel() != "claude-haiku-4-5" {
		t.Errorf("default model mismatch: %q", c.GetActiveModel())
	}
	c.SetActiveModel("claude-sonnet-4-6")
	if c.GetActiveModel() != "claude-sonnet-4-6" {
		t.Errorf("SetActiveModel did not stick: %q", c.GetActiveModel())
	}
	c.SetBaseURL("https://example.com/")
	if c.GetBaseURL() != "https://example.com" {
		t.Errorf("SetBaseURL should trim trailing slash, got %q", c.GetBaseURL())
	}
}

func TestStripJSONFences(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"a":1}`, `{"a":1}`},
		{"```json\n{\"a\":1}\n```", `{"a":1}`},
		{"```\n{\"a\":1}\n```", `{"a":1}`},
		{"  ```json\n{\"nested\":{\"b\":2}}\n```  ", `{"nested":{"b":2}}`},
		{"not fenced at all", "not fenced at all"},
	}
	for _, tc := range cases {
		if got := stripJSONFences(tc.in); got != tc.want {
			t.Errorf("stripJSONFences(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
