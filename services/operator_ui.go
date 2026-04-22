package services

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grove-platform/github-copier/configs"
)

//go:embed web/operator/index.html
var operatorIndexHTML []byte

var operatorVersionTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// RegisterOperatorRoutes mounts the operator HTML UI and JSON APIs under /operator/.
// Call only when cfg.OperatorUIEnabled is true. Works with any HTTP origin (local
// dev, Cloud Run, Kubernetes, etc.). Every secured API requires an Authorization:
// Bearer <github-pat> header. The user's permission on cfg.OperatorAuthRepo
// determines their role (operator or writer).
func RegisterOperatorRoutes(mux *http.ServeMux, cfg *configs.Config, container *ServiceContainer, version string) {
	o := &operatorUI{
		cfg:       cfg,
		container: container,
		version:   version,
		ghCache:   newGHAuthCache(5 * time.Minute),
		// 30 suggestions/hour/PAT caps Anthropic spend per operator. Normal
		// usage is well under this; a misbehaving client can't rack up a bill.
		suggestLimiter: newTokenBucket(30, time.Hour),
	}
	// Always create the LLM client; availability is checked dynamically via Ping.
	// Operators can change the active model and base URL from the UI without restart.
	if client, err := NewLLMClient(LLMClientOptions{
		Provider: cfg.LLMProvider,
		BaseURL:  cfg.LLMBaseURL,
		Model:    cfg.LLMModel,
		APIKey:   cfg.AnthropicAPIKey,
	}); err != nil {
		LogWarning("LLM client init failed", "error", err.Error())
	} else {
		o.llm = client
		LogInfo("LLM rule suggester ready", "provider", client.ProviderName(), "base_url", cfg.LLMBaseURL, "model", cfg.LLMModel, "note", "availability checked at request time")
	}
	// Register specific paths before the /operator/ subtree so /operator/api/* is not handled by serveIndex.
	mux.HandleFunc("/operator/api/status", o.handleOperatorStatus)
	mux.HandleFunc("/operator/api/audit/events", o.wrapAPI(o.handleAuditEvents))
	mux.HandleFunc("/operator/api/audit/overview", o.wrapAPI(o.handleAuditOverview))
	mux.HandleFunc("/operator/api/observability/deliveries", o.wrapAPI(o.handleObservabilityDeliveries))
	mux.HandleFunc("/operator/api/observability/webhook-traces", o.wrapAPI(o.handleObservabilityWebhookTraces))
	mux.HandleFunc("/operator/api/deployment", o.wrapAPI(o.handleDeployment))
	mux.HandleFunc("/operator/api/release", o.wrapOperatorOnly(o.handleRelease))
	mux.HandleFunc("/operator/api/replay", o.wrapOperatorOnly(o.handleReplay))
	mux.HandleFunc("/operator/api/workflows", o.wrapAPI(o.handleWorkflows))
	mux.HandleFunc("/operator/api/logs", o.wrapAPI(o.handleDeliveryLogs))
	mux.HandleFunc("/operator/api/me", o.wrapAPI(o.handleMe))
	mux.HandleFunc("/operator/api/repo-permission", o.wrapAPI(o.handleRepoPermission))
	mux.HandleFunc("/operator/api/suggest-rule", o.wrapAPI(o.handleSuggestRule))
	mux.HandleFunc("/operator/api/llm/status", o.wrapAPI(o.handleLLMStatus))
	mux.HandleFunc("/operator/api/llm/settings", o.wrapOperatorOnly(o.handleLLMSettings))
	mux.HandleFunc("/operator/api/llm/model", o.wrapOperatorOnly(o.handleLLMDeleteModel))
	mux.HandleFunc("/operator/api/llm/pull", o.wrapOperatorOnly(o.handleLLMPullModel))
	mux.HandleFunc("/operator/", o.serveIndex)
	mux.HandleFunc("/operator", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/operator/", http.StatusFound)
	})
	LogInfo("Operator UI: /operator/ with GitHub PAT authentication", "auth_repo", cfg.OperatorAuthRepo)
}

type operatorUI struct {
	cfg            *configs.Config
	container      *ServiceContainer
	version        string
	replayInFlight sync.Map     // key: "owner/repo#pr" → prevents concurrent replays
	ghCache        *ghAuthCache // GitHub PAT validation + per-repo permission cache
	llm            LLMClient    // optional: enabled when cfg.LLMEnabled is true
	suggestLimiter *tokenBucket // per-PAT rate limit for /api/suggest-rule (LLM cost cap)
	llmPing        llmPingCache // cached Ping() result so /llm/status doesn't burn tokens on every refresh
}

// llmPingCache memoises the most recent LLMClient.Ping() outcome. Status-tab
// refreshes don't need fresh liveness data more than once every 30s, and
// each uncached ping costs one input + one output Anthropic token.
type llmPingCache struct {
	mu        sync.RWMutex
	err       error
	checkedAt time.Time
}

func (p *llmPingCache) get(ttl time.Duration) (err error, ok bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.checkedAt.IsZero() || time.Since(p.checkedAt) > ttl {
		return nil, false
	}
	return p.err, true
}

func (p *llmPingCache) set(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
	p.checkedAt = time.Now()
}

// invalidate forces the next get() to miss, so operators who change the
// base URL or active model see fresh liveness state on the next refresh.
func (p *llmPingCache) invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = nil
	p.checkedAt = time.Time{}
}

// operatorUserCtxKey is the context key for the authenticated operator user.
type operatorUserCtxKey struct{}

// operatorUserFromCtx returns the authenticated user from the request context (nil if not set).
func operatorUserFromCtx(r *http.Request) *OperatorUser {
	u, _ := r.Context().Value(operatorUserCtxKey{}).(*OperatorUser)
	return u
}

// wrapAPI validates the incoming request's GitHub PAT and attaches the user to the context.
func (o *operatorUI) wrapAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		token := bearerToken(r)
		if token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "provide a GitHub Personal Access Token as Bearer token"})
			return
		}
		user, err := o.authenticateGitHub(r.Context(), token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		ctx := context.WithValue(r.Context(), operatorUserCtxKey{}, user)
		next(w, r.WithContext(ctx))
	}
}

// wrapOperatorOnly wraps a handler that requires the "operator" role (replay, release).
func (o *operatorUI) wrapOperatorOnly(next http.HandlerFunc) http.HandlerFunc {
	return o.wrapAPI(func(w http.ResponseWriter, r *http.Request) {
		user := operatorUserFromCtx(r)
		if user == nil || user.Role != RoleOperator {
			role := "unknown"
			if user != nil {
				role = string(user.Role)
			}
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("this action requires operator access (you have %s)", role),
			})
			return
		}
		next(w, r)
	})
}

func (o *operatorUI) authenticateGitHub(ctx context.Context, pat string) (*OperatorUser, error) {
	if o.ghCache != nil {
		if user, err, ok := o.ghCache.get(pat); ok {
			return user, err
		}
	}
	authCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	user, err := validateGitHubPAT(authCtx, pat, o.cfg.OperatorAuthRepo)
	if o.ghCache != nil {
		o.ghCache.set(pat, user, err)
	}
	return user, err
}

// handleOperatorStatus reports whether secured operator APIs are configured (no auth).
func (o *operatorUI) handleOperatorStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	out := map[string]any{
		"operator_apis_enabled": true,
		"auth_repo":             o.cfg.OperatorAuthRepo,
		"llm_available":         o.llm != nil, // client exists; reachability checked via /operator/api/llm/status
		"metrics_enabled":       o.cfg.MetricsEnabled,
		"audit_enabled":         o.cfg.AuditEnabled,
		"version":               o.version,
	}
	if o.container != nil && o.container.DeliveryTracker != nil {
		out["webhook_dedupe_entries"] = o.container.DeliveryTracker.Len()
		out["webhook_recent_observations"] = o.container.DeliveryTracker.HistoryLen()
	}
	if o.container != nil && o.container.WebhookTraces != nil {
		out["webhook_trace_entries"] = o.container.WebhookTraces.Len()
	}
	_ = json.NewEncoder(w).Encode(out)
}

// handleMe returns the authenticated user's GitHub login, avatar, and role.
func (o *operatorUI) handleMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	user := operatorUserFromCtx(r)
	if user == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no authenticated user in context"})
		return
	}
	_ = json.NewEncoder(w).Encode(user)
}

// handleRepoPermission reports whether the authenticated user has read access to a given repo.
// Used by the frontend to pre-check replay eligibility per source repo.
// Query params: repos=owner/repo1,owner/repo2 (comma-separated).
func (o *operatorUI) handleRepoPermission(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	reposParam := strings.TrimSpace(r.URL.Query().Get("repos"))
	if reposParam == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "repos query param required"})
		return
	}
	repos := strings.Split(reposParam, ",")

	// Per-repo result: Allowed + optional Error. Surfacing the error lets
	// the frontend distinguish "user genuinely can't read this repo" from
	// "GitHub rate limited us" so disabled replay buttons can carry an
	// actionable tooltip instead of an opaque gray state.
	type repoPerm struct {
		Allowed bool   `json:"allowed"`
		Error   string `json:"error,omitempty"`
	}
	result := make(map[string]repoPerm, len(repos))

	user := operatorUserFromCtx(r)
	userPAT := bearerToken(r)
	if user == nil || userPAT == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	for _, repo := range repos {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		canRead, err := o.ghCache.CanUserReadRepo(ctx, userPAT, user.Login, repo)
		entry := repoPerm{Allowed: canRead}
		if err != nil {
			entry.Error = err.Error()
		}
		result[repo] = entry
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"permissions": result})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func (o *operatorUI) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/operator/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(operatorIndexHTML)
}

func (o *operatorUI) handleAuditEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	q, err := parseAuditListQuery(r)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if o.container.AuditLogger == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{}})
		return
	}
	events, err := o.container.AuditLogger.QueryAuditEvents(ctx, q)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
}

func parseAuditListQuery(r *http.Request) (AuditListQuery, error) {
	q := r.URL.Query()
	lim := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lim = n
		}
	}
	if lim > 200 {
		lim = 200
	}
	aq := AuditListQuery{Limit: lim}
	if et := strings.TrimSpace(q.Get("event_type")); et != "" {
		switch AuditEventType(et) {
		case AuditEventCopy, AuditEventDeprecation, AuditEventError:
			aq.EventType = et
		default:
			return AuditListQuery{}, fmt.Errorf("invalid event_type (use copy, deprecation, or error)")
		}
	}
	switch strings.TrimSpace(strings.ToLower(q.Get("success"))) {
	case "true":
		t := true
		aq.Success = &t
	case "false":
		f := false
		aq.Success = &f
	case "":
	default:
		return AuditListQuery{}, fmt.Errorf("invalid success (use true or false)")
	}
	if rn := strings.TrimSpace(q.Get("rule_name")); rn != "" {
		aq.RuleName = rn
	}
	if prStr := strings.TrimSpace(q.Get("pr_number")); prStr != "" {
		n, err := strconv.Atoi(prStr)
		if err != nil || n <= 0 {
			return AuditListQuery{}, fmt.Errorf("pr_number must be a positive integer")
		}
		aq.PRNumber = &n
	}
	if ps := strings.TrimSpace(q.Get("path")); ps != "" {
		aq.PathSearch = ps
	}
	if since := strings.TrimSpace(q.Get("since")); since != "" {
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return AuditListQuery{}, fmt.Errorf("since must be RFC3339: %w", err)
		}
		aq.Since = &t
	}
	return aq, nil
}

func (o *operatorUI) handleAuditOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	days := 14
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	if days > 366 {
		days = 366
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if o.container.AuditLogger == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"days":           days,
			"daily_volume":   []DailyStats{},
			"stats_by_rule":  map[string]RuleStats{},
			"audit_disabled": true,
		})
		return
	}
	daily, err1 := o.container.AuditLogger.GetDailyVolume(ctx, days)
	if err1 != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err1.Error()})
		return
	}
	byRule, err2 := o.container.AuditLogger.GetStatsByRule(ctx)
	if err2 != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err2.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"days":          days,
		"daily_volume":  daily,
		"stats_by_rule": byRule,
	})
}

func (o *operatorUI) handleObservabilityDeliveries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	max := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			max = n
		}
	}
	if max > deliveryHistoryMax {
		max = deliveryHistoryMax
	}
	if o.container.DeliveryTracker == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"deliveries": []DeliverySnapshot{}})
		return
	}
	snap := o.container.DeliveryTracker.RecentDeliveries(max)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"deliveries":     snap,
		"dedupe_entries": o.container.DeliveryTracker.Len(),
	})
}

func (o *operatorUI) handleObservabilityWebhookTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	max := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			max = n
		}
	}
	if max > webhookTraceMaxEntries {
		max = webhookTraceMaxEntries
	}
	if o.container == nil || o.container.WebhookTraces == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"traces": []WebhookTraceEntry{}})
		return
	}
	tr := o.container.WebhookTraces.Recent(max)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"traces": tr,
		"total":  o.container.WebhookTraces.Len(),
	})
}

// OperatorDeploymentInfo is non-secret runtime and platform metadata for the operator UI.
type OperatorDeploymentInfo struct {
	Version            string            `json:"version"`
	UptimeSeconds      int64             `json:"uptime_seconds"`
	MongoHealthy       *bool             `json:"mongo_healthy,omitempty"`
	GoogleCloudRegion  string            `json:"google_cloud_region,omitempty"`
	CloudRunService    string            `json:"cloud_run_service,omitempty"`
	CloudRunRevision   string            `json:"cloud_run_revision,omitempty"`
	CloudRunConfig     string            `json:"cloud_run_configuration,omitempty"`
	GoogleCloudProject string            `json:"google_cloud_project,omitempty"`
	Port               string            `json:"port"`
	WebhookPath        string            `json:"webhook_path"`
	DryRun             bool              `json:"dry_run"`
	AuditEnabled       bool              `json:"audit_enabled"`
	AuditDatabase      string            `json:"audit_database,omitempty"`
	AuditCollection    string            `json:"audit_collection,omitempty"`
	ConfigRepo         string            `json:"config_repo,omitempty"`
	EffectiveConfig    string            `json:"effective_config_file,omitempty"`
	OperatorRepoSlug   string            `json:"operator_repo_slug,omitempty"`
	ReleaseAPIMode     ReleaseAPIMode    `json:"release_api_mode"`
	Env                map[string]string `json:"cloud_env,omitempty"`
}

// ReleaseAPIMode describes whether the operator UI can cut a release tag.
// Typed so the set of possible values is discoverable from the type alone
// and so the frontend can switch on a known enum instead of a free string.
type ReleaseAPIMode string

const (
	// ReleaseAPIDisabled — neither OPERATOR_RELEASE_GITHUB_TOKEN nor
	// OPERATOR_REPO_SLUG is configured; the UI hides the release button.
	ReleaseAPIDisabled ReleaseAPIMode = "disabled"
	// ReleaseAPITagCreateEnabled — both are configured; the UI shows the
	// release flow and /api/release will attempt to create a tag ref.
	ReleaseAPITagCreateEnabled ReleaseAPIMode = "tag_create_enabled"
)

func (o *operatorUI) handleDeployment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	releaseMode := ReleaseAPIDisabled
	if o.cfg.OperatorReleaseGitHubToken != "" && o.cfg.OperatorRepoSlug != "" {
		releaseMode = ReleaseAPITagCreateEnabled
	}
	info := OperatorDeploymentInfo{
		Version:            o.version,
		UptimeSeconds:      int64(time.Since(o.container.StartTime).Seconds()),
		CloudRunService:    os.Getenv("K_SERVICE"),
		CloudRunRevision:   os.Getenv("K_REVISION"),
		CloudRunConfig:     os.Getenv("K_CONFIGURATION"),
		GoogleCloudProject: o.cfg.GoogleCloudProjectId,
		Port:               o.cfg.Port,
		WebhookPath:        o.cfg.WebserverPath,
		DryRun:             o.cfg.DryRun,
		AuditEnabled:       o.cfg.AuditEnabled,
		AuditDatabase:      o.cfg.AuditDatabase,
		AuditCollection:    o.cfg.AuditCollection,
		ConfigRepo:         o.cfg.ConfigRepoOwner + "/" + o.cfg.ConfigRepoName,
		EffectiveConfig:    o.cfg.EffectiveConfigFile(),
		OperatorRepoSlug:   o.cfg.OperatorRepoSlug,
		ReleaseAPIMode:     releaseMode,
		Env: map[string]string{
			"ENV": firstEnv("ENV"),
		},
	}
	if region := os.Getenv("GOOGLE_CLOUD_REGION"); region != "" {
		info.GoogleCloudRegion = region
	}
	if o.cfg.AuditEnabled && o.container.AuditLogger != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		healthy := o.container.AuditLogger.Ping(ctx) == nil
		info.MongoHealthy = &healthy
	}
	_ = json.NewEncoder(w).Encode(info)
}

type operatorReleaseRequest struct {
	Version string `json:"version"`
}

type operatorReleaseResponse struct {
	OK      bool   `json:"ok,omitempty"`
	Ref     string `json:"ref,omitempty"`
	TagSHA  string `json:"tag_sha,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
	Notice  string `json:"notice,omitempty"`
}

func (o *operatorUI) handleRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReleaseResponse{Error: "read body"})
		return
	}
	var req operatorReleaseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReleaseResponse{Error: "invalid json"})
		return
	}
	v := strings.TrimSpace(req.Version)
	if !operatorVersionTagRe.MatchString(v) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReleaseResponse{Error: "version must match vMAJOR.MINOR.PATCH"})
		return
	}
	if o.cfg.OperatorReleaseGitHubToken == "" || o.cfg.OperatorRepoSlug == "" {
		w.WriteHeader(http.StatusNotImplemented)
		_ = json.NewEncoder(w).Encode(operatorReleaseResponse{
			Error:  "set OPERATOR_RELEASE_GITHUB_TOKEN and OPERATOR_REPO_SLUG to enable tag creation from the UI",
			Notice: "Full releases (changelog + GitHub Release) still use ./scripts/release.sh locally.",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	ref, sha, err := githubCreateVersionTag(ctx, o.cfg.OperatorReleaseGitHubToken, o.cfg.OperatorRepoSlug, o.cfg.OperatorReleaseTargetBranch, v)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(operatorReleaseResponse{Error: err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(operatorReleaseResponse{
		OK:      true,
		Ref:     ref,
		TagSHA:  sha,
		Message: "Tag pushed to GitHub; if CI is configured for tag deploys, the pipeline should start shortly.",
		Notice:  "This does not update CHANGELOG.md — use scripts/release.sh for a documented release.",
	})
}

// ── Per-delivery log viewer ──

func (o *operatorUI) handleDeliveryLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	deliveryID := strings.TrimSpace(r.URL.Query().Get("delivery_id"))
	if deliveryID == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "delivery_id is required"})
		return
	}
	if o.container.DeliveryLogs == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"logs": []LogEntry{}, "delivery_id": deliveryID})
		return
	}
	logs := o.container.DeliveryLogs.Get(deliveryID)
	if logs == nil {
		logs = []LogEntry{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"logs": logs, "delivery_id": deliveryID})
}

// ── Workflow config browser ──

func (o *operatorUI) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	if o.container.ConfigLoader == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "config loader not initialized"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	yamlCfg, err := o.container.ConfigLoader.LoadConfig(ctx, o.cfg)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":     "failed to load config: " + err.Error(),
			"workflows": []any{},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"workflows":   yamlCfg.Workflows,
		"defaults":    yamlCfg.Defaults,
		"config_file": o.cfg.EffectiveConfigFile(),
		"config_repo": o.cfg.ConfigRepoOwner + "/" + o.cfg.ConfigRepoName,
	})
}

// ── Webhook replay ──

type operatorReplayRequest struct {
	Repo      string `json:"repo"` // "owner/repo"
	PRNumber  int    `json:"pr_number"`
	Branch    string `json:"branch"`     // base branch
	CommitSHA string `json:"commit_sha"` // optional — fetched from GitHub if empty
}

type operatorReplayResponse struct {
	OK      bool   `json:"ok,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (o *operatorUI) handleReplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "read body"})
		return
	}
	var req operatorReplayRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "invalid json"})
		return
	}

	// Validate inputs
	parts := strings.SplitN(strings.TrimSpace(req.Repo), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "repo must be owner/repo"})
		return
	}
	owner, repoName := parts[0], parts[1]

	if req.PRNumber <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "pr_number must be > 0"})
		return
	}
	if strings.TrimSpace(req.Branch) == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "branch is required"})
		return
	}

	// Source-repo permission check: the user's PAT must have at least read
	// access to the source repo being replayed.
	{
		user := operatorUserFromCtx(r)
		userPAT := bearerToken(r)
		if user == nil || userPAT == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "unauthenticated"})
			return
		}
		permCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		canRead, permErr := o.ghCache.CanUserReadRepo(permCtx, userPAT, user.Login, req.Repo)
		cancel()
		if !canRead {
			w.WriteHeader(http.StatusForbidden)
			msg := fmt.Sprintf("you do not have access to source repo %s", req.Repo)
			if permErr != nil {
				msg = fmt.Sprintf("%s: %s", msg, permErr.Error())
			}
			_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: msg})
			return
		}
	}

	// In-flight dedup: prevent concurrent replays for the same PR
	replayKey := fmt.Sprintf("%s#%d", req.Repo, req.PRNumber)
	if _, loaded := o.replayInFlight.LoadOrStore(replayKey, true); loaded {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "replay already in progress for this PR"})
		return
	}

	// Fetch commit SHA from GitHub if not provided
	commitSHA := strings.TrimSpace(req.CommitSHA)
	if commitSHA == "" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		client, err := GetRestClientForOrg(ctx, o.cfg, owner)
		if err != nil {
			o.replayInFlight.Delete(replayKey)
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "github auth: " + err.Error()})
			return
		}
		pr, _, err := client.PullRequests.Get(ctx, owner, repoName, req.PRNumber)
		if err != nil {
			o.replayInFlight.Delete(replayKey)
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "fetch PR: " + err.Error()})
			return
		}
		if !pr.GetMerged() {
			o.replayInFlight.Delete(replayKey)
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "PR is not merged — only merged PRs can be replayed"})
			return
		}
		commitSHA = pr.GetMergeCommitSHA()
		if commitSHA == "" {
			o.replayInFlight.Delete(replayKey)
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(operatorReplayResponse{Error: "PR has no merge commit SHA"})
			return
		}
	}

	// Dispatch replay in background (same path as real webhook processing)
	// Millisecond timestamps alone collide when two operators replay in the
	// same ms (rare but observed in tests). Append a short random suffix so
	// the delivery ID is unique across concurrent replays on the same revision.
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	deliveryID := fmt.Sprintf("replay-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(rnd[:]))
	baseBranch := strings.TrimSpace(req.Branch)

	LogInfo("operator replay requested",
		"repo", req.Repo,
		"pr_number", req.PRNumber,
		"branch", baseBranch,
		"commit_sha", commitSHA,
		"delivery_id", deliveryID,
	)

	AppendWebhookTrace(o.container, WebhookTraceEntry{
		DeliveryID: deliveryID,
		EventType:  "operator_replay",
		Repo:       req.Repo,
		BaseBranch: baseBranch,
		CommitSHA:  commitSHA,
		PRNumber:   req.PRNumber,
		Outcome:    "replay_started",
		Detail:     "initiated via operator UI",
	})

	bgCtx := context.Background()
	if o.container.DeliveryLogs != nil {
		bgCtx = ContextWithLogBuffer(bgCtx, deliveryID, o.container.DeliveryLogs)
	}
	if o.cfg.WebhookProcessingTimeoutSeconds > 0 {
		var cancel context.CancelFunc
		bgCtx, cancel = context.WithTimeout(bgCtx, time.Duration(o.cfg.WebhookProcessingTimeoutSeconds)*time.Second)
		o.container.wg.Add(1)
		go func() {
			defer o.container.wg.Done()
			defer cancel()
			defer o.replayInFlight.Delete(replayKey)
			processWebhookWithRetry(bgCtx, req.PRNumber, commitSHA, owner, repoName, baseBranch, deliveryID, o.cfg, o.container)
		}()
	} else {
		o.container.wg.Add(1)
		go func() {
			defer o.container.wg.Done()
			defer o.replayInFlight.Delete(replayKey)
			processWebhookWithRetry(bgCtx, req.PRNumber, commitSHA, owner, repoName, baseBranch, deliveryID, o.cfg, o.container)
		}()
	}

	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(operatorReplayResponse{
		OK:      true,
		Message: fmt.Sprintf("Replay started for %s PR #%d (delivery %s). Check webhook traces for progress.", req.Repo, req.PRNumber, deliveryID),
	})
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// ghBranchNameRe matches branch names permitted by GitHub: no spaces, control
// chars, or the handful of reserved characters (~ ^ : ? * [ \). The regex is
// intentionally narrower than GitHub's full rules — it's a defense-in-depth
// check before we embed the value in an API path, not a validator.
var ghBranchNameRe = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,120}$`)

func githubCreateVersionTag(ctx context.Context, pat, repoSlug, baseBranch, version string) (ref string, sha string, err error) {
	parts := strings.SplitN(strings.TrimSpace(repoSlug), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid OPERATOR_REPO_SLUG (want owner/repo)")
	}
	owner, repo := parts[0], parts[1]
	// Defense-in-depth: even though these come from env vars and not user
	// input, validate them against the same whitelists ghAPIGetRepoPermission
	// uses before embedding in API paths. Apply url.PathEscape for the same
	// reason. Keeps the gosec story consistent across the package.
	if !ghUsernameRe.MatchString(owner) {
		return "", "", fmt.Errorf("invalid owner in OPERATOR_REPO_SLUG %q", repoSlug)
	}
	if !ghRepoNameRe.MatchString(repo) {
		return "", "", fmt.Errorf("invalid repo name in OPERATOR_REPO_SLUG %q", repoSlug)
	}
	if !ghBranchNameRe.MatchString(baseBranch) {
		return "", "", fmt.Errorf("invalid OPERATOR_RELEASE_TARGET_BRANCH %q", baseBranch)
	}
	baseURL := fmt.Sprintf(
		"%s/repos/%s/%s/git/ref/heads/%s",
		githubAPIBaseURL,
		url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(baseBranch),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil) // #nosec G107 -- githubAPIBaseURL is binary-controlled; path components validated above
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := sharedGithubHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	baseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("github get branch ref: %s: %s", resp.Status, strings.TrimSpace(string(baseBody)))
	}
	var refObj struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(baseBody, &refObj); err != nil {
		return "", "", fmt.Errorf("parse branch ref: %w", err)
	}
	headSHA := refObj.Object.SHA
	if headSHA == "" {
		return "", "", fmt.Errorf("empty base sha for branch %s", baseBranch)
	}

	tagRef := "refs/tags/" + version
	payload := map[string]string{"ref": tagRef, "sha": headSHA}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", "", err
	}
	postURL := fmt.Sprintf("%s/repos/%s/%s/git/refs", githubAPIBaseURL, url.PathEscape(owner), url.PathEscape(repo))
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(buf)) // #nosec G107 -- githubAPIBaseURL is binary-controlled; path components validated above
	if err != nil {
		return "", "", err
	}
	postReq.Header.Set("Authorization", "Bearer "+pat)
	postReq.Header.Set("Accept", "application/vnd.github+json")
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	postResp, err := sharedGithubHTTPClient.Do(postReq)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = postResp.Body.Close() }()
	postBody, _ := io.ReadAll(io.LimitReader(postResp.Body, 1<<20))
	if postResp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("github create tag ref: %s: %s", postResp.Status, strings.TrimSpace(string(postBody)))
	}
	var created struct {
		Ref    string `json:"ref"`
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(postBody, &created); err != nil {
		return "", "", fmt.Errorf("parse create ref response: %w", err)
	}
	return created.Ref, created.Object.SHA, nil
}

// sharedGithubHTTPClient is reused for all operator-originated GitHub API
// calls (release tagging, etc.). Reusing one *http.Client amortizes the
// underlying transport's connection pool.
var sharedGithubHTTPClient = &http.Client{Timeout: 25 * time.Second}
