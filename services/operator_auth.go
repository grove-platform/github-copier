package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ghAPIError is returned by the GitHub API helper calls on any non-2xx
// response. Callers can inspect StatusCode to distinguish transient 5xx
// failures (should not flip authorization decisions) from 4xx responses
// (definitive "no access").
type ghAPIError struct {
	StatusCode int
	Body       string
}

func (e *ghAPIError) Error() string {
	return fmt.Sprintf("GitHub API HTTP %d: %s", e.StatusCode, e.Body)
}

func (e *ghAPIError) IsTransient() bool { return e.StatusCode >= 500 }

// hashToken returns the SHA-256 hex digest of a PAT. Used as the cache key so
// raw tokens never sit in the process heap beyond the lifetime of a single
// request — a memory dump of the running server won't leak active tokens.
func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// githubAPIBaseURL is the base for GitHub REST API calls. Package var (rather
// than a const) so tests can point it at an httptest.Server. Never set from
// user input — the SSRF surface for ghAPIGet* is unchanged.
var githubAPIBaseURL = "https://api.github.com"

// ghUsernameRe matches valid GitHub usernames: alphanumeric + hyphens,
// cannot start or end with a hyphen, max 39 chars. Used to reject hostile
// input before it reaches URL construction for the GitHub API. (RE2 has no
// lookahead, so this doesn't reject consecutive hyphens — that's a GitHub
// policy issue, not a security one; such requests simply fail downstream.)
var ghUsernameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

// ghRepoNameRe matches valid GitHub repo names.
var ghRepoNameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)

// OperatorRole represents the permission level for the operator UI.
type OperatorRole string

const (
	// RoleOperator has full access: view, replay, release. Operators are
	// trusted with the full repo topology and bypass per-repo scoping in
	// the read-only views (audit events, webhook traces, delivery logs,
	// workflow config).
	RoleOperator OperatorRole = "operator"
	// RoleWriter has read-only access: view workflows, audit, recent copies.
	// Read-only views are post-filtered by repoFilter so writers only see
	// rows that reference repos they can read on GitHub — without this they
	// could enumerate every source→target pairing and audit row in the
	// system, regardless of whether they have GitHub access to those repos.
	RoleWriter OperatorRole = "writer"
	// RoleDenied means the user has no access.
	RoleDenied OperatorRole = "denied"
)

// OperatorUser represents an authenticated operator UI user.
type OperatorUser struct {
	Login     string       `json:"login"`
	AvatarURL string       `json:"avatar_url,omitempty"`
	Role      OperatorRole `json:"role"`
}

// ghAuthCache caches GitHub PAT validation results to avoid hitting the API on every request.
// It also caches per-repo permission lookups (one permission level per token+repo pair).
type ghAuthCache struct {
	mu       sync.RWMutex
	entries  map[string]*ghAuthEntry
	repoPerm map[string]*ghRepoPermEntry // key: token + "\x00" + repo
	ttl      time.Duration
}

type ghAuthEntry struct {
	user      *OperatorUser
	err       error
	expiresAt time.Time
}

type ghRepoPermEntry struct {
	permission string // "admin", "maintain", "write", "triage", "read", or "" for denied
	err        error
	expiresAt  time.Time
}

func newGHAuthCache(ttl time.Duration) *ghAuthCache {
	return &ghAuthCache{
		entries:  make(map[string]*ghAuthEntry),
		repoPerm: make(map[string]*ghRepoPermEntry),
		ttl:      ttl,
	}
}

// Cache methods take raw tokens and hash them internally, so callers never
// have to think about the token→digest boundary. Raw tokens never become
// map keys.

func (c *ghAuthCache) get(token string) (*OperatorUser, error, bool) {
	key := hashToken(token)
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, nil, false
	}
	return e.user, e.err, true
}

func (c *ghAuthCache) set(token string, user *OperatorUser, err error) {
	key := hashToken(token)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = &ghAuthEntry{
		user:      user,
		err:       err,
		expiresAt: time.Now().Add(c.ttl),
	}
	// Evict expired entries periodically (simple sweep when cache grows)
	if len(c.entries) > 100 {
		now := time.Now()
		for k, v := range c.entries {
			if now.After(v.expiresAt) {
				delete(c.entries, k)
			}
		}
	}
}

func (c *ghAuthCache) getRepoPerm(token, repo string) (string, error, bool) {
	key := hashToken(token) + "\x00" + repo
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.repoPerm[key]
	if !ok || time.Now().After(e.expiresAt) {
		return "", nil, false
	}
	return e.permission, e.err, true
}

func (c *ghAuthCache) setRepoPerm(token, repo, permission string, err error) {
	key := hashToken(token) + "\x00" + repo
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repoPerm[key] = &ghRepoPermEntry{
		permission: permission,
		err:        err,
		expiresAt:  time.Now().Add(c.ttl),
	}
	if len(c.repoPerm) > 500 {
		now := time.Now()
		for k, v := range c.repoPerm {
			if now.After(v.expiresAt) {
				delete(c.repoPerm, k)
			}
		}
	}
}

// CanUserReadRepo returns true if the user (identified by PAT) has at least read access to the repo.
// Uses the cache when available. Returns (hasAccess, error).
func (c *ghAuthCache) CanUserReadRepo(ctx context.Context, pat, username, repo string) (bool, error) {
	if perm, err, ok := c.getRepoPerm(pat, repo); ok {
		if err != nil {
			return false, err
		}
		return permissionGrantsRead(perm), nil
	}
	perm, err := ghAPIGetRepoPermission(ctx, pat, repo, username)
	c.setRepoPerm(pat, repo, perm, err)
	if err != nil {
		return false, err
	}
	return permissionGrantsRead(perm), nil
}

func permissionGrantsRead(perm string) bool {
	switch perm {
	case "admin", "maintain", "write", "triage", "read":
		return true
	}
	return false
}

// validateGitHubPAT validates a GitHub PAT and returns the authenticated user with their role.
// It calls the GitHub API to get the user info, then checks their permission on the auth repo.
func validateGitHubPAT(ctx context.Context, pat string, authRepo string) (*OperatorUser, error) {
	if pat == "" {
		return nil, fmt.Errorf("empty token")
	}

	// 1. Get the authenticated user
	ghUser, err := ghAPIGetUser(ctx, pat)
	if err != nil {
		return nil, fmt.Errorf("validate token: %w", err)
	}

	user := &OperatorUser{
		Login:     ghUser.Login,
		AvatarURL: ghUser.AvatarURL,
		Role:      RoleWriter, // default to read-only
	}

	// authRepo is required in github mode (enforced at config load via
	// validateOperatorAuth). This guard is defensive only.
	if authRepo == "" {
		return nil, fmt.Errorf("OPERATOR_AUTH_REPO is not configured")
	}

	// 2. Check the user's permission on the auth repo.
	//
	// Authorization posture: only a transient GitHub outage (5xx) lets the
	// caller through with the default writer role — otherwise a GitHub
	// hiccup locks out every legitimate operator. Every other failure
	// (404 "not a collaborator", 401/403, network error, parse error)
	// denies access. This closes the "any valid PAT gets writer" hole that
	// existed when we soft-failed on all errors.
	perm, err := ghAPIGetRepoPermission(ctx, pat, authRepo, ghUser.Login)
	if err != nil {
		var apiErr *ghAPIError
		if errors.As(err, &apiErr) && apiErr.IsTransient() {
			LogWarning("GitHub permission check transiently failed, keeping writer role",
				"user", ghUser.Login, "repo", authRepo, "status", apiErr.StatusCode)
			return user, nil
		}
		user.Role = RoleDenied
		return user, fmt.Errorf("user %s has no access to %s: %w", ghUser.Login, authRepo, err)
	}

	// admin/maintain → operator; write/triage/read → writer. "write" is
	// deliberately NOT operator: most writers have write access to the
	// auth repo, so mapping write → operator would give every writer the
	// ability to replay and cut releases. Operator actions require an
	// explicit admin or maintain grant.
	switch perm {
	case "admin", "maintain":
		user.Role = RoleOperator
	case "write", "triage", "read":
		user.Role = RoleWriter
	default:
		user.Role = RoleDenied
		return user, fmt.Errorf("user %s has no access to %s", ghUser.Login, authRepo)
	}

	return user, nil
}

// ghUserResponse is the minimal response from GET /user.
type ghUserResponse struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

func ghAPIGetUser(ctx context.Context, pat string) (*ghUserResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBaseURL+"/user", nil) // #nosec G107 -- githubAPIBaseURL is set by the binary, not user input
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	if resp.StatusCode != http.StatusOK {
		return nil, &ghAPIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	var user ghUserResponse
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("parse user response: %w", err)
	}
	if user.Login == "" {
		return nil, fmt.Errorf("empty login in GitHub response")
	}
	return &user, nil
}

// ghPermissionResponse is the response from GET /repos/{owner}/{repo}/collaborators/{user}/permission.
type ghPermissionResponse struct {
	Permission string `json:"permission"`
}

func ghAPIGetRepoPermission(ctx context.Context, pat string, repo string, username string) (string, error) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid repo format: %s (expected owner/repo)", repo)
	}
	// Validate path components against strict whitelists before URL construction.
	// Host is hardcoded to api.github.com — not user-controlled.
	if !ghUsernameRe.MatchString(parts[0]) {
		return "", fmt.Errorf("invalid owner in repo %q", repo)
	}
	if !ghRepoNameRe.MatchString(parts[1]) {
		return "", fmt.Errorf("invalid repo name in %q", repo)
	}
	if !ghUsernameRe.MatchString(username) {
		return "", fmt.Errorf("invalid username %q", username)
	}
	apiURL := fmt.Sprintf(
		"%s/repos/%s/%s/collaborators/%s/permission",
		githubAPIBaseURL, url.PathEscape(parts[0]), url.PathEscape(parts[1]), url.PathEscape(username),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil) // #nosec G107 G704 -- host is hardcoded to api.github.com; path components validated above
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req) // #nosec G107 G704 -- host is hardcoded to api.github.com; path components validated above
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	if resp.StatusCode != http.StatusOK {
		return "", &ghAPIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	var perm ghPermissionResponse
	if err := json.Unmarshal(body, &perm); err != nil {
		return "", fmt.Errorf("parse permission response: %w", err)
	}
	return perm.Permission, nil
}
