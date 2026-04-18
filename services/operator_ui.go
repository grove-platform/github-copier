package services

import (
	"bytes"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/grove-platform/github-copier/configs"
)

//go:embed web/operator/index.html
var operatorIndexHTML []byte

var operatorVersionTagRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// RegisterOperatorRoutes mounts the operator HTML UI and JSON APIs under /operator/.
// cfg.OperatorUIToken must be non-empty before calling (caller checks).
func RegisterOperatorRoutes(mux *http.ServeMux, cfg *configs.Config, container *ServiceContainer, version string) {
	o := &operatorUI{
		cfg:       cfg,
		container: container,
		version:   version,
	}
	mux.HandleFunc("/operator/api/audit/events", o.wrapAPI(o.handleAuditEvents))
	mux.HandleFunc("/operator/api/deployment", o.wrapAPI(o.handleDeployment))
	mux.HandleFunc("/operator/api/release", o.wrapAPI(o.handleRelease))
	mux.HandleFunc("/operator/", o.serveIndex)
	mux.HandleFunc("/operator", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/operator/", http.StatusFound)
	})
}

type operatorUI struct {
	cfg       *configs.Config
	container *ServiceContainer
	version   string
}

func (o *operatorUI) wrapAPI(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !operatorAuthOK(o.cfg.OperatorUIToken, bearerToken(r)) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

func operatorAuthOK(expected, got string) bool {
	if expected == "" {
		return false
	}
	e := []byte(expected)
	g := []byte(got)
	if len(e) != len(g) {
		return false
	}
	return subtle.ConstantTimeCompare(e, g) == 1
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
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if o.container.AuditLogger == nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []any{}})
		return
	}
	events, err := o.container.AuditLogger.GetRecentEvents(ctx, limit)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": events})
}

// OperatorDeploymentInfo is non-secret runtime and platform metadata for the operator UI.
type OperatorDeploymentInfo struct {
	Version            string            `json:"version"`
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
	ReleaseAPIMode     string            `json:"release_api_mode"`
	Env                map[string]string `json:"cloud_env,omitempty"`
}

func (o *operatorUI) handleDeployment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "method not allowed"})
		return
	}
	releaseMode := "disabled"
	if o.cfg.OperatorReleaseGitHubToken != "" && o.cfg.OperatorRepoSlug != "" {
		releaseMode = "tag_create_enabled"
	}
	info := OperatorDeploymentInfo{
		Version:            o.version,
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

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func githubCreateVersionTag(ctx context.Context, pat, repoSlug, baseBranch, version string) (ref string, sha string, err error) {
	parts := strings.SplitN(strings.TrimSpace(repoSlug), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid OPERATOR_REPO_SLUG (want owner/repo)")
	}
	owner, repo := parts[0], parts[1]
	baseURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/ref/heads/%s", owner, repo, baseBranch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := githubHTTPClient().Do(req)
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
	postURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/git/refs", owner, repo)
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(buf))
	if err != nil {
		return "", "", err
	}
	postReq.Header.Set("Authorization", "Bearer "+pat)
	postReq.Header.Set("Accept", "application/vnd.github+json")
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	postResp, err := githubHTTPClient().Do(postReq)
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

func githubHTTPClient() *http.Client {
	return &http.Client{Timeout: 25 * time.Second}
}
