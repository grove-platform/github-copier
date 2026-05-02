package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHashToken(t *testing.T) {
	a := hashToken("secret-pat-abc")
	b := hashToken("secret-pat-abc")
	c := hashToken("secret-pat-xyz")
	if a != b {
		t.Fatalf("same input must produce same digest: %s vs %s", a, b)
	}
	if a == c {
		t.Fatalf("different inputs must produce different digests")
	}
	if strings.Contains(a, "secret") {
		t.Fatalf("digest leaks plaintext: %s", a)
	}
	if len(a) != 64 {
		t.Fatalf("expected 64-char sha256 hex digest, got %d chars", len(a))
	}
}

func TestGHAPIError_IsTransient(t *testing.T) {
	cases := []struct {
		status    int
		transient bool
	}{
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusNotFound, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		e := &ghAPIError{StatusCode: tc.status}
		if got := e.IsTransient(); got != tc.transient {
			t.Errorf("status %d: IsTransient()=%v, want %v", tc.status, got, tc.transient)
		}
	}
}

func TestPermissionGrantsRead(t *testing.T) {
	readers := []string{"admin", "maintain", "write", "triage", "read"}
	nonReaders := []string{"", "none", "denied", "unknown"}
	for _, p := range readers {
		if !permissionGrantsRead(p) {
			t.Errorf("permission %q must grant read", p)
		}
	}
	for _, p := range nonReaders {
		if permissionGrantsRead(p) {
			t.Errorf("permission %q must NOT grant read", p)
		}
	}
}

// stubGitHub replaces githubAPIBaseURL with an httptest.Server that returns
// the given /user and /permission responses. Returns a cleanup func.
type stubResponses struct {
	userStatus int
	userBody   string
	permStatus int
	permBody   string
}

func stubGitHub(t *testing.T, rs stubResponses) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/user":
			w.WriteHeader(rs.userStatus)
			_, _ = w.Write([]byte(rs.userBody))
		case strings.HasPrefix(r.URL.Path, "/repos/") && strings.HasSuffix(r.URL.Path, "/permission"):
			w.WriteHeader(rs.permStatus)
			_, _ = w.Write([]byte(rs.permBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	prev := githubAPIBaseURL
	githubAPIBaseURL = srv.URL
	return func() {
		githubAPIBaseURL = prev
		srv.Close()
	}
}

func TestValidateGitHubPAT_RoleMapping(t *testing.T) {
	cases := []struct {
		name     string
		perm     string
		wantRole OperatorRole
		wantErr  bool
	}{
		{"admin maps to operator", "admin", RoleOperator, false},
		{"maintain maps to operator", "maintain", RoleOperator, false},
		{"write maps to writer (not operator)", "write", RoleWriter, false},
		{"triage maps to writer", "triage", RoleWriter, false},
		{"read maps to writer", "read", RoleWriter, false},
		{"unknown permission denies", "mystery", RoleDenied, true},
		{"empty permission denies", "", RoleDenied, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanup := stubGitHub(t, stubResponses{
				userStatus: http.StatusOK,
				userBody:   `{"login":"alice","avatar_url":"https://example.com/a.png"}`,
				permStatus: http.StatusOK,
				permBody:   fmt.Sprintf(`{"permission":%q}`, tc.perm),
			})
			defer cleanup()

			user, err := validateGitHubPAT(context.Background(), "pat-123", "org/repo")
			if tc.wantErr && err == nil {
				t.Fatalf("want error for perm=%q, got none", tc.perm)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for perm=%q: %v", tc.perm, err)
			}
			if user == nil {
				t.Fatalf("want non-nil user")
			}
			if user.Role != tc.wantRole {
				t.Errorf("perm=%q: role=%q, want %q", tc.perm, user.Role, tc.wantRole)
			}
		})
	}
}

// Critical test for the review finding: a 404 from the permission check must
// deny access (not soft-fail to writer). This prevents "any valid PAT → writer".
func TestValidateGitHubPAT_PermissionCheck404_Denies(t *testing.T) {
	cleanup := stubGitHub(t, stubResponses{
		userStatus: http.StatusOK,
		userBody:   `{"login":"mallory","avatar_url":""}`,
		permStatus: http.StatusNotFound,
		permBody:   `{"message":"Not Found"}`,
	})
	defer cleanup()

	user, err := validateGitHubPAT(context.Background(), "pat-xyz", "org/repo")
	if err == nil {
		t.Fatalf("want error when user is not a collaborator (404), got nil")
	}
	if user == nil || user.Role != RoleDenied {
		var role OperatorRole
		if user != nil {
			role = user.Role
		}
		t.Fatalf("want RoleDenied on 404, got %q", role)
	}
}

// 5xx from GitHub is treated as transient — users keep their default writer
// role so a GitHub outage doesn't lock everyone out. The audit log captures
// the event; the cache TTL bounds exposure.
func TestValidateGitHubPAT_PermissionCheck5xx_KeepsWriter(t *testing.T) {
	cleanup := stubGitHub(t, stubResponses{
		userStatus: http.StatusOK,
		userBody:   `{"login":"bob","avatar_url":""}`,
		permStatus: http.StatusInternalServerError,
		permBody:   `upstream error`,
	})
	defer cleanup()

	user, err := validateGitHubPAT(context.Background(), "pat-abc", "org/repo")
	if err != nil {
		t.Fatalf("5xx must not surface an error to the caller (soft-fail): %v", err)
	}
	if user == nil || user.Role != RoleWriter {
		var role OperatorRole
		if user != nil {
			role = user.Role
		}
		t.Fatalf("want RoleWriter on 5xx soft-fail, got %q", role)
	}
}

// An invalid / expired PAT (401 on /user) must deny, not soft-fail.
func TestValidateGitHubPAT_UserLookup401_Denies(t *testing.T) {
	cleanup := stubGitHub(t, stubResponses{
		userStatus: http.StatusUnauthorized,
		userBody:   `{"message":"Bad credentials"}`,
	})
	defer cleanup()

	user, err := validateGitHubPAT(context.Background(), "expired-pat", "org/repo")
	if err == nil {
		t.Fatalf("want error for invalid PAT (401), got nil")
	}
	if user != nil {
		t.Errorf("want nil user on failed token validation, got %+v", user)
	}

	var apiErr *ghAPIError
	if !errors.As(err, &apiErr) {
		t.Errorf("want wrapped ghAPIError, got %T: %v", err, err)
	} else if apiErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("want StatusCode=401, got %d", apiErr.StatusCode)
	}
}

func TestGHAuthCache_UsesHashedKeys(t *testing.T) {
	c := newGHAuthCache(5 * 60)
	pat := "super-secret-pat-12345"
	user := &OperatorUser{Login: "alice", Role: RoleOperator}
	c.set(pat, user, nil)

	// The raw token must not appear as a key — only its sha256 digest.
	c.mu.RLock()
	defer c.mu.RUnlock()
	for k := range c.entries {
		if strings.Contains(k, pat) {
			t.Fatalf("cache key leaks raw PAT: %q", k)
		}
	}
	if _, ok := c.entries[hashToken(pat)]; !ok {
		t.Fatalf("expected cache entry under hashed key")
	}
}
