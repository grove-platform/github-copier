package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// handleLLMStatus returns the current LLM settings, reachability, and installed models.
func (o *operatorUI) handleLLMStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	out := map[string]any{
		"available":    o.llm != nil,
		"provider":     o.cfg.LLMProvider,
		"base_url":     "",
		"active_model": "",
		"reachable":    false,
		"models":       []LLMModel{},
	}
	if o.llm == nil {
		out["error"] = "LLM client not initialized"
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	out["base_url"] = o.llm.GetBaseURL()
	out["active_model"] = o.llm.GetActiveModel()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := o.llm.Ping(ctx); err != nil {
		out["error"] = err.Error()
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	out["reachable"] = true

	models, err := o.llm.ListModels(ctx)
	if err != nil {
		out["error"] = "list models: " + err.Error()
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	out["models"] = models
	_ = json.NewEncoder(w).Encode(out)
}

// handleLLMSettings updates the active model and/or base URL at runtime.
// In-memory only — reverts to env-var defaults on process restart.
type llmSettingsRequest struct {
	ActiveModel string `json:"active_model,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
}

func (o *operatorUI) handleLLMSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	if o.llm == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "LLM client not initialized"})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	var req llmSettingsRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	if m := strings.TrimSpace(req.ActiveModel); m != "" {
		o.llm.SetActiveModel(m)
	}
	if u := strings.TrimSpace(req.BaseURL); u != "" {
		o.llm.SetBaseURL(u)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active_model": o.llm.GetActiveModel(),
		"base_url":     o.llm.GetBaseURL(),
	})
}

// handleLLMDeleteModel deletes a model from the LLM server.
func (o *operatorUI) handleLLMDeleteModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	if o.llm == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "LLM client not initialized"})
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "name query param required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := o.llm.DeleteModel(ctx, name); err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "deleted": name})
}

// handleLLMPullModel streams pull progress to the client as NDJSON.
// Each line is a JSON object with {status, completed, total, error}.
func (o *operatorUI) handleLLMPullModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	if o.llm == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "LLM client not initialized"})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid json"})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "name is required"})
		return
	}

	// Switch to NDJSON streaming
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering when behind a proxy
	flusher, canFlush := w.(http.Flusher)
	encoder := json.NewEncoder(w)

	// Pulls can take a long time; don't use r.Context() if the client could disconnect
	// prematurely. Use a 20-minute timeout as a safety net.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	// Still honor client cancellation
	go func() {
		<-r.Context().Done()
		cancel()
	}()

	err := o.llm.PullModel(ctx, req.Name, func(ev LLMPullProgress) {
		_ = encoder.Encode(ev)
		if canFlush {
			flusher.Flush()
		}
	})
	if err != nil {
		_ = encoder.Encode(LLMPullProgress{Error: fmt.Sprintf("pull failed: %s", err.Error())})
		if canFlush {
			flusher.Flush()
		}
		return
	}
	// Final event so the client knows the stream ended successfully
	_ = encoder.Encode(LLMPullProgress{Status: "done"})
	if canFlush {
		flusher.Flush()
	}
}
