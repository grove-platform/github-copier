// test-llm exercises the operator UI's LLM client end-to-end against the
// configured provider. It loads LLM_PROVIDER / LLM_BASE_URL / LLM_MODEL /
// ANTHROPIC_API_KEY from the environment (or an env file via -env), calls
// Ping, ListModels, and a minimal GenerateJSON with the real rule-suggester
// prompt, and prints the result. Useful for verifying a gateway URL or
// rotated API key before deploying.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/grove-platform/github-copier/services"
)

func main() {
	envFile := flag.String("env", "", "Optional path to a .env file to load before running")
	timeout := flag.Duration("timeout", 30*time.Second, "Per-call timeout")
	flag.Parse()

	if *envFile != "" {
		if err := godotenv.Load(*envFile); err != nil {
			fmt.Fprintf(os.Stderr, "❌ failed to load %s: %v\n", *envFile, err)
			os.Exit(1)
		}
		fmt.Printf("Loaded env file: %s\n", *envFile)
	}

	provider := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_PROVIDER")))
	if provider == "" {
		provider = "ollama"
	}
	baseURL := os.Getenv("LLM_BASE_URL")
	model := os.Getenv("LLM_MODEL")
	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	fmt.Printf("Provider: %s\n", provider)
	fmt.Printf("Base URL: %s\n", defaultIfEmpty(baseURL, "(default)"))
	fmt.Printf("Model:    %s\n", defaultIfEmpty(model, "(default)"))
	if provider == "anthropic" {
		fmt.Printf("API key:  %s\n", redact(apiKey))
	}
	fmt.Println()

	client, err := services.NewLLMClient(services.LLMClientOptions{
		Provider: provider,
		BaseURL:  baseURL,
		Model:    model,
		APIKey:   apiKey,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ NewLLMClient: %v\n", err)
		os.Exit(1)
	}

	// 1. Ping
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	if err := client.Ping(ctx); err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "❌ Ping: %v\n", err)
		os.Exit(1)
	}
	cancel()
	fmt.Println("✅ Ping OK")

	// 2. ListModels
	ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	models, err := client.ListModels(ctx)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠️  ListModels: %v\n", err)
	} else {
		fmt.Printf("✅ ListModels: %d models\n", len(models))
		for _, m := range models {
			fmt.Printf("   - %s\n", m.Name)
		}
	}

	// 3. GenerateJSON using the real rule-suggester system prompt. Importing
	// services.SuggestRuleSystemPrompt keeps the smoke test in lock-step with
	// what writers hit via the UI — if the prompt changes, the smoke test
	// covers the new behavior automatically.
	systemPrompt := services.SuggestRuleSystemPrompt
	userPrompt := `Generate a copier rule for this transformation:

Source file: agg/python/models/user.py
Target file: shared/python/models/user.py
Target repo: org/shared-examples

Return ONLY a JSON object with the fields documented above. No prose outside the JSON.`

	ctx, cancel = context.WithTimeout(context.Background(), *timeout)
	raw, err := client.GenerateJSON(ctx, systemPrompt, userPrompt)
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ GenerateJSON: %v\n", err)
		os.Exit(1)
	}

	// Pretty-print if the response parses as JSON; otherwise show raw.
	var pretty map[string]any
	if jerr := json.Unmarshal([]byte(raw), &pretty); jerr == nil {
		out, _ := json.MarshalIndent(pretty, "   ", "  ")
		fmt.Printf("✅ GenerateJSON parsed OK:\n   %s\n", out)
	} else {
		fmt.Printf("⚠️  GenerateJSON returned non-JSON (%v):\n%s\n", jerr, raw)
		os.Exit(1)
	}

	fmt.Println("\n🎉 All checks passed — the LLM provider is reachable and usable.")
}

func defaultIfEmpty(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func redact(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 8 {
		return "***"
	}
	return s[:4] + "…" + s[len(s)-4:]
}
