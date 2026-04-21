package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OperatorRole represents the permission level for the operator UI.
type OperatorRole string

const (
	// RoleOperator has full access: view, replay, release.
	RoleOperator OperatorRole = "operator"
	// RoleWriter has read-only access: view workflows, audit, recent copies.
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

func (c *ghAuthCache) get(token string) (*OperatorUser, error, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[token]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, nil, false
	}
	return e.user, e.err, true
}

func (c *ghAuthCache) set(token string, user *OperatorUser, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[token] = &ghAuthEntry{
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
	key := token + "\x00" + repo
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.repoPerm[key]
	if !ok || time.Now().After(e.expiresAt) {
		return "", nil, false
	}
	return e.permission, e.err, true
}

func (c *ghAuthCache) setRepoPerm(token, repo, permission string, err error) {
	key := token + "\x00" + repo
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

	// 2. If no auth repo configured, grant operator access to any valid GitHub user
	if authRepo == "" {
		user.Role = RoleOperator
		return user, nil
	}

	// 3. Check the user's permission on the auth repo
	perm, err := ghAPIGetRepoPermission(ctx, pat, authRepo, ghUser.Login)
	if err != nil {
		// If we can't check permissions (repo not found, no access), default to writer
		LogWarning("GitHub permission check failed, defaulting to writer role",
			"user", ghUser.Login, "repo", authRepo, "error", err)
		return user, nil
	}

	switch perm {
	case "admin", "maintain", "write":
		user.Role = RoleOperator
	case "read", "triage":
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
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

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("invalid or expired GitHub token (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/collaborators/%s/permission", parts[0], parts[1], username)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+pat)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("permission check: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var perm ghPermissionResponse
	if err := json.Unmarshal(body, &perm); err != nil {
		return "", fmt.Errorf("parse permission response: %w", err)
	}
	return perm.Permission, nil
}
