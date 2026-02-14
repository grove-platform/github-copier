package test

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jarcoal/httpmock"

	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/services"
	"github.com/grove-platform/github-copier/types"
)

//
// Environment helpers
//

// EnvOwnerRepo returns config repo owner/name from env and fails the test if either is missing.
func EnvOwnerRepo(t testing.TB) (string, string) {
	t.Helper()
	owner := os.Getenv(configs.ConfigRepoOwner)
	repo := os.Getenv(configs.ConfigRepoName)
	if owner == "" || repo == "" {
		t.Fatalf("CONFIG_REPO_OWNER/CONFIG_REPO_NAME not set")
	}
	return owner, repo
}

//
// HTTP/test wiring helpers
//

// WithHTTPMock wraps a test in `httpmock` activation on a dedicated http.Client
// and routes the TokenManager's HTTP client through it.
func WithHTTPMock(t testing.TB) *http.Client {
	t.Helper()
	c := &http.Client{}
	httpmock.ActivateNonDefault(c)
	t.Cleanup(func() { httpmock.DeactivateAndReset() })

	tm := services.DefaultTokenManager()
	prev := tm.GetHTTPClient()
	tm.SetHTTPClient(c)
	t.Cleanup(func() { tm.SetHTTPClient(prev) })
	return c
}

// DumpHttpmockCalls logs all recorded httpmock keys and counts. Used for debugging while writing tests.
func DumpHttpmockCalls(t testing.TB) {
	t.Helper()
	for k, v := range httpmock.GetCallCountInfo() {
		t.Logf("httpmock key: %q -> %d", k, v)
	}
}

//
// Mock registration helpers
//

// MockGitHubAppTokenEndpoint mocks the GitHub App installation token endpoint with a fixed fake token.
func MockGitHubAppTokenEndpoint(installationID string) {
	httpmock.RegisterResponder("POST",
		"https://api.github.com/app/installations/"+installationID+"/access_tokens",
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"token": "test-installation-token"}),
	)
}

// MockGitHubAppInstallations mocks the GitHub App installations list endpoint.
func MockGitHubAppInstallations(orgToInstallationID map[string]string) {
	installations := []map[string]any{}
	for org, installID := range orgToInstallationID {
		installations = append(installations, map[string]any{
			"id": installID,
			"account": map[string]any{
				"login": org,
				"type":  "Organization",
			},
		})
	}
	httpmock.RegisterResponder("GET",
		"https://api.github.com/app/installations",
		httpmock.NewJsonResponderOrPanic(200, installations),
	)
}

// SetupOrgToken sets up a cached installation token for an organization.
// This bypasses the need to mock the installations and token endpoints.
func SetupOrgToken(org, token string) {
	services.SetInstallationTokenForOrg(org, token)
}

// MockGitHubWriteEndpoints mocks the full direct-commit flow endpoints for a single branch.
// Returns the URLs for the base ref, commits, and update ref endpoints.
func MockGitHubWriteEndpoints(owner, repo, branch string) (baseRefURL, commitsURL, updateRefURL string) {
	baseRefURL = "https://api.github.com/repos/" + owner + "/" + repo + "/git/ref/heads/" + branch
	httpmock.RegisterResponder("GET", baseRefURL,
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/" + branch,
			"object": map[string]any{
				"sha": "baseSha",
			},
		}),
	)

	treesRe := regexp.MustCompile(`^https://api\.github\.com/repos/` + regexp.QuoteMeta(owner) + `/` +
		regexp.QuoteMeta(repo) + `/git/trees(\?.*)?$`)
	httpmock.RegisterRegexpResponder("POST", treesRe,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{
			"sha": "newTreeSha",
		}),
	)

	commitsURL = "https://api.github.com/repos/" + owner + "/" + repo + "/git/commits"
	httpmock.RegisterResponder("POST", commitsURL,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{
			"sha": "newCommitSha",
		}),
	)

	updateRefURL = "https://api.github.com/repos/" + owner + "/" + repo + "/git/refs/heads/" + branch
	httpmock.RegisterResponder("PATCH", updateRefURL,
		httpmock.NewStringResponder(200, `{}`),
	)

	return
}

// MockContentsEndpoint mocks GET file contents for a given path/ref.
func MockContentsEndpoint(owner, repo, path, contentB64 string) {
	re := regexp.MustCompile(
		`^https://api\.github\.com/repos/` + regexp.QuoteMeta(owner) + `/` +
			regexp.QuoteMeta(repo) + `/contents/` + regexp.QuoteMeta(path) +
			`\?ref=(?:main|SRC_BRANCH|release/[0-9.]+)$`,
	)
	httpmock.RegisterRegexpResponder("GET", re,
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"type":     "file",
			"encoding": "base64",
			"path":     path,
			"content":  contentB64,
		}),
	)
}

// MockCreateRef mocks POST to create a new temp branch ref. Returns the exact URL for call-count asserts.
func MockCreateRef(owner, repo string) string {
	url := "https://api.github.com/repos/" + owner + "/" + repo + "/git/refs"
	httpmock.RegisterResponder("POST", url,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{
			"ref":    "refs/heads/copier/20250101-000000",
			"object": map[string]any{"sha": "baseSha"},
		}),
	)
	return url
}

// MockPullsAndMerge mocks creating and merging a PR.
func MockPullsAndMerge(owner, repo string, number int) {
	httpmock.RegisterResponder("POST",
		"https://api.github.com/repos/"+owner+"/"+repo+"/pulls",
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"number": number}),
	)
	httpmock.RegisterResponder("PUT",
		"https://api.github.com/repos/"+owner+"/"+repo+fmt.Sprintf("/pulls/%d/merge", number),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{"merged": true}),
	)
}

// MockDeleteTempRef mocks DELETE to remove a temporary branch ref.
func MockDeleteTempRef(owner, repo string) {
	re := regexp.MustCompile(
		`^https://api\.github\.com/repos/` + regexp.QuoteMeta(owner) + `/` +
			regexp.QuoteMeta(repo) + `/git/refs/heads/copier/\d{8}-\d{6}$`,
	)
	httpmock.RegisterRegexpResponder("DELETE", re, httpmock.NewStringResponder(204, ""))
}

//
// Staging/assertion helpers
//

// NormalizeUpload flattens a FilesToUpload map to UploadKey -> []names for simpler comparisons.
func NormalizeUpload(in map[types.UploadKey]types.UploadFileContent) map[types.UploadKey][]string {
	out := make(map[types.UploadKey][]string, len(in))
	for k, v := range in {
		names := make([]string, 0, len(v.Content))
		for _, c := range v.Content {
			names = append(names, c.GetName())
		}
		out[k] = names
	}
	return out
}

// MakeChanged is a shorthand to build ChangedFile entries.
func MakeChanged(status, path string) types.ChangedFile {
	return types.ChangedFile{Status: status, Path: path}
}

// CountByMethodAndURLRegexp adds up call counts for a given METHOD whose stored httpmock key's URL matches urlRE.
func CountByMethodAndURLRegexp(method string, urlRE *regexp.Regexp) int {
	info := httpmock.GetCallCountInfo()
	total := 0
	for k, v := range info {
		if !(strings.HasPrefix(k, method+" ") || strings.HasPrefix(k, method+"=~")) {
			continue
		}
		var urlish string
		switch {
		case strings.HasPrefix(k, method+"=~"):
			urlish = strings.TrimPrefix(k, method+"=~")
		case strings.HasPrefix(k, method+" "):
			urlish = strings.TrimPrefix(k, method+" ")
		default:
			continue
		}
		urlish = strings.Trim(urlish, "^$")
		urlish = strings.ReplaceAll(urlish, `\`, "")
		if urlRE.MatchString(urlish) {
			total += v
		}
	}
	return total
}

// GetRefGetCount counts GET calls to /git/ref/(refs/)?heads/<branch>
// for the given owner/repo/branch.
func GetRefGetCount(owner, repo, branch string) int {
	re := regexp.MustCompile(`/repos/` + regexp.QuoteMeta(owner) + `/` + regexp.QuoteMeta(repo) +
		`/git/ref/(?:refs/)?heads/` + regexp.QuoteMeta(branch) + `$`)
	return CountByMethodAndURLRegexp("GET", re)
}
