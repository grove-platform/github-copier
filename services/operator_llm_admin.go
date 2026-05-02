package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// llmBaseURLAllowedHostsEnv lets deployments pin the set of hosts an operator
// can route the LLM client at (comma-separated, host[:port], case-insensitive).
// Unset = no host pinning; scheme rules below still apply.
const llmBaseURLAllowedHostsEnv = "LLM_BASE_URL_ALLOWED_HOSTS"

// validateLLMBaseURL enforces scheme + host rules on operator-supplied LLM base
// URLs. Hosted providers (anthropic) ship a bearer credential to whatever host
// the client points at, so we require https and reject userinfo / opaque URIs;
// otherwise a malicious operator could exfiltrate the API key by setting the
// base URL to a host they control. Ollama is exempt from the https requirement
// because the legitimate default is http://localhost:11434 for local dev.
//
// Returns the cleaned URL (trailing slash trimmed) or an error suitable for a
// 400 response. allowedHosts may be empty to skip host pinning.
func validateLLMBaseURL(provider, raw string, allowedHosts []string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("base_url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("base_url is not a valid URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("base_url must be an absolute URL with scheme and host")
	}
	// Reject userinfo (https://attacker@victim.com/) and opaque forms — both
	// confuse host-based allowlisting and have no legitimate use here.
	if u.User != nil {
		return "", fmt.Errorf("base_url must not contain userinfo")
	}
	if u.Opaque != "" {
		return "", fmt.Errorf("base_url must not be opaque")
	}
	scheme := strings.ToLower(u.Scheme)
	prov := strings.ToLower(strings.TrimSpace(provider))
	switch prov {
	case "anthropic":
		if scheme != "https" {
			return "", fmt.Errorf("base_url for anthropic must use https (got %q)", scheme)
		}
	case "", "ollama":
		// Local dev commonly uses http://localhost:11434.
		if scheme != "http" && scheme != "https" {
			return "", fmt.Errorf("base_url must use http or https (got %q)", scheme)
		}
	default:
		if scheme != "https" {
			return "", fmt.Errorf("base_url must use https (got %q)", scheme)
		}
	}
	if len(allowedHosts) > 0 {
		host := strings.ToLower(u.Host)
		ok := false
		for _, h := range allowedHosts {
			if strings.EqualFold(strings.TrimSpace(h), host) {
				ok = true
				break
			}
		}
		if !ok {
			return "", fmt.Errorf("base_url host %q is not in the allowlist", u.Host)
		}
	}
	return strings.TrimSuffix(raw, "/"), nil
}

// llmBaseURLAllowedHosts reads and parses LLM_BASE_URL_ALLOWED_HOSTS. Returns
// nil when unset so callers know to skip host pinning.
func llmBaseURLAllowedHosts() []string {
	raw := strings.TrimSpace(os.Getenv(llmBaseURLAllowedHostsEnv))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

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
		// supports_model_mgmt tells the UI whether to show pull/delete sections.
		// Hosted providers (anthropic) don't expose those operations.
		"supports_model_mgmt": strings.ToLower(strings.TrimSpace(o.cfg.LLMProvider)) != "anthropic",
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

	// Cache the ping outcome for 30s. For Anthropic this saves real tokens
	// (every refresh of the status tab used to hit /v1/messages); for
	// Ollama it saves an /api/tags round-trip. handleLLMSettings clears the
	// entry when base URL / model change so operators see fresh state.
	pingErr, ok := o.llmPing.get(30 * time.Second)
	if !ok {
		pingErr = o.llm.Ping(ctx)
		o.llmPing.set(pingErr)
	}
	if pingErr != nil {
		out["error"] = pingErr.Error()
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
	// Capture pre-change state for the audit record. This must happen before
	// any setter call so the "before" value reflects what the operator changed
	// from, not what they changed to.
	oldModel := o.llm.GetActiveModel()
	oldBaseURL := o.llm.GetBaseURL()
	changed := false
	newModel := oldModel
	newBaseURL := oldBaseURL
	if m := strings.TrimSpace(req.ActiveModel); m != "" && m != oldModel {
		o.llm.SetActiveModel(m)
		newModel = o.llm.GetActiveModel()
		changed = true
	}
	if u := strings.TrimSpace(req.BaseURL); u != "" {
		// Validate before applying. The Anthropic client ships the bearer
		// credential to whatever host the base URL points at — without
		// scheme/host validation, an operator could redirect the credential
		// to a host they control.
		cleaned, err := validateLLMBaseURL(o.cfg.LLMProvider, u, llmBaseURLAllowedHosts())
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if cleaned != oldBaseURL {
			o.llm.SetBaseURL(cleaned)
			newBaseURL = o.llm.GetBaseURL()
			changed = true
		}
	}
	// Invalidate the ping cache on mutation so the next /llm/status call
	// re-checks liveness against the new config — otherwise an operator
	// flipping the URL sees a stale "connected" line for up to 30s.
	if changed {
		o.llmPing.invalidate()
		o.recordLLMSettingsAudit(r, oldBaseURL, newBaseURL, oldModel, newModel)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"active_model": o.llm.GetActiveModel(),
		"base_url":     o.llm.GetBaseURL(),
	})
}

// recordLLMSettingsAudit emits a structured log line and (when MongoDB audit
// logging is enabled) persists a config_change event capturing who changed
// what. This is the detection backstop for the SetBaseURL credential-exfil
// risk: scheme/host validation blocks the obvious cases, but a persisted
// trail of every successful change lets responders spot abuse after the fact.
func (o *operatorUI) recordLLMSettingsAudit(r *http.Request, oldBaseURL, newBaseURL, oldModel, newModel string) {
	actor := ""
	if u := operatorUserFromCtx(r); u != nil {
		actor = u.Login
	}
	LogInfo("operator changed LLM settings",
		"actor", actor,
		"provider", o.cfg.LLMProvider,
		"old_base_url", oldBaseURL,
		"new_base_url", newBaseURL,
		"old_model", oldModel,
		"new_model", newModel,
	)
	if o.container == nil || o.container.AuditLogger == nil {
		return
	}
	// Use a detached short-timeout context: writing the audit row must not
	// fail just because the client disconnected after receiving the 200.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev := &AuditEvent{
		Actor:   actor,
		Success: true,
		AdditionalData: map[string]any{
			"setting":      "llm",
			"provider":     o.cfg.LLMProvider,
			"old_base_url": oldBaseURL,
			"new_base_url": newBaseURL,
			"old_model":    oldModel,
			"new_model":    newModel,
		},
	}
	if err := o.container.AuditLogger.LogConfigChangeEvent(ctx, ev); err != nil {
		LogWarning("audit LogConfigChangeEvent failed", "error", err)
	}
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
		status := http.StatusBadGateway
		if errors.Is(err, ErrModelManagementNotSupported) {
			status = http.StatusBadRequest
		}
		w.WriteHeader(status)
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
	// Reject up-front for hosted providers so the client doesn't have to interpret
	// an NDJSON error event.
	if strings.ToLower(strings.TrimSpace(o.cfg.LLMProvider)) == "anthropic" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": ErrModelManagementNotSupported.Error()})
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
