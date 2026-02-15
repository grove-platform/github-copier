package services_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/configs"
	"github.com/grove-platform/github-copier/services"
	"github.com/grove-platform/github-copier/types"
	"github.com/jarcoal/httpmock"
	"github.com/stretchr/testify/require"

	test "github.com/grove-platform/github-copier/tests"
)

func TestMain(m *testing.M) {
	_ = os.Setenv(configs.ConfigRepoOwner, "my-org")
	_ = os.Setenv(configs.ConfigRepoName, "config-repo")
	_ = os.Setenv(configs.InstallationId, "12345")
	_ = os.Setenv(configs.AppId, "1166559")
	_ = os.Setenv(configs.AppClientId, "IvTestClientId")
	_ = os.Setenv("SKIP_SECRET_MANAGER", "true")
	_ = os.Setenv(configs.ConfigRepoBranch, "main")

	key, _ := rsa.GenerateKey(rand.Reader, 1024)
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	_ = os.Setenv("GITHUB_APP_PRIVATE_KEY", string(pemBytes))
	_ = os.Setenv("GITHUB_APP_PRIVATE_KEY_B64", base64.StdEncoding.EncodeToString(pemBytes))

	code := m.Run()

	_ = os.Unsetenv(configs.ConfigRepoOwner)
	_ = os.Unsetenv(configs.ConfigRepoName)
	_ = os.Unsetenv(configs.InstallationId)
	_ = os.Unsetenv(configs.AppId)
	_ = os.Unsetenv(configs.AppClientId)
	_ = os.Unsetenv("SKIP_SECRET_MANAGER")
	_ = os.Unsetenv("SRC_BRANCH")
	_ = os.Unsetenv("GITHUB_APP_PRIVATE_KEY")
	_ = os.Unsetenv("GITHUB_APP_PRIVATE_KEY_B64")

	os.Exit(code)
}

func TestAddFilesToTargetRepos_Direct_Succeeds(t *testing.T) {
	_ = test.WithHTTPMock(t)

	owner, repo := test.EnvOwnerRepo(t)
	branch := "main"

	test.SetupOrgToken(owner, "test-token")

	baseRefURL, commitsURL, updateRefURL := test.MockGitHubWriteEndpoints(owner, repo, branch)

	files := []github.RepositoryContent{
		{
			Name:    github.Ptr("dir/example1.txt"),
			Path:    github.Ptr("dir/example1.txt"),
			Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("hello 1"))),
		},
		{
			Name:    github.Ptr("dir/example2.txt"),
			Path:    github.Ptr("dir/example2.txt"),
			Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("hello 2"))),
		},
	}
	filesToUpload := map[types.UploadKey]types.UploadFileContent{
		{RepoName: repo, BranchPath: "refs/heads/" + branch}: {
			TargetBranch: branch,
			Content:      files,
		},
	}

	services.AddFilesToTargetRepos(context.Background(), test.TestConfig(), filesToUpload, nil, nil)

	info := httpmock.GetCallCountInfo()
	require.Equal(t, 1, info["GET "+baseRefURL])

	treeCalls := 0
	for k, v := range info {
		if strings.HasPrefix(k, "POST https://api.github.com/repos/"+owner+"/"+repo+"/git/trees") {
			treeCalls += v
		}
	}
	require.Equal(t, 1, treeCalls)
	require.Equal(t, 1, info["POST "+commitsURL])
	require.Equal(t, 1, info["PATCH "+updateRefURL])
}

func TestAddFilesToTargetRepos_ViaPR_Succeeds(t *testing.T) {
	_ = test.WithHTTPMock(t)
	t.Setenv("COPIER_COMMIT_STRATEGY", "pr")

	owner, repo := test.EnvOwnerRepo(t)
	baseBranch := "main"

	// Force fresh token
	services.DefaultTokenManager().SetInstallationAccessToken("")
	cfg := test.TestConfig()
	test.MockGitHubAppTokenEndpoint(cfg.InstallationId)
	err := services.ConfigurePermissions(context.Background(), cfg)
	require.NoError(t, err, "ConfigurePermissions should succeed")

	test.SetupOrgToken(owner, "test-token")

	// Base ref used to create temp branch
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/ref/(?:refs/)?heads/`+baseBranch+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/" + baseBranch, "object": map[string]any{"sha": "baseSha"},
		}),
	)

	createRefURL := test.MockCreateRef(owner, repo)

	tempHead := `copier/\d{8}-\d{6}`
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/ref/(?:refs/)?heads/`+tempHead+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/copier/20250101-000000", "object": map[string]any{"sha": "baseSha"},
		}),
	)
	httpmock.RegisterRegexpResponder("POST",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/trees(\?.*)?$`),
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "newTreeSha"}),
	)
	commitsURL := "https://api.github.com/repos/" + owner + "/" + repo + "/git/commits"
	httpmock.RegisterResponder("POST", commitsURL,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "newCommitSha"}),
	)
	httpmock.RegisterRegexpResponder("PATCH",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/refs/heads/`+tempHead+`$`),
		httpmock.NewStringResponder(200, "{}"),
	)

	test.MockPullsAndMerge(owner, repo, 42)
	test.MockDeleteTempRef(owner, repo)

	files := []github.RepositoryContent{
		{
			Name:    github.Ptr("dir/example1.txt"),
			Path:    github.Ptr("dir/example1.txt"),
			Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("hello 1"))),
		},
		{
			Name:    github.Ptr("dir/example2.txt"),
			Path:    github.Ptr("dir/example2.txt"),
			Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("hello 2"))),
		},
	}
	filesToUpload := map[types.UploadKey]types.UploadFileContent{
		{RepoName: repo, BranchPath: "refs/heads/" + baseBranch}: {
			TargetBranch:   baseBranch,
			Content:        files,
			CommitStrategy: "pr",
			AutoMergePR:    true,
		},
	}

	services.AddFilesToTargetRepos(context.Background(), cfg, filesToUpload, nil, nil)

	require.Equal(t, 1, test.CountByMethodAndURLRegexp("POST",
		regexp.MustCompile(`/app/installations/`+regexp.QuoteMeta(cfg.InstallationId)+`/access_tokens$`),
	))
	info := httpmock.GetCallCountInfo()
	require.Equal(t, 1, info["POST "+createRefURL])

	require.Equal(t, 1, test.CountByMethodAndURLRegexp("POST",
		regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/pulls$`),
	))
	require.Equal(t, 1, test.CountByMethodAndURLRegexp("PUT",
		regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/pulls/42/merge$`),
	))
	require.Equal(t, 1, info["POST "+commitsURL])

	require.GreaterOrEqual(t,
		test.CountByMethodAndURLRegexp("GET",
			regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/git/ref/(?:refs/)?heads/`+regexp.QuoteMeta(baseBranch)+`$`)),
		1,
	)
	require.GreaterOrEqual(t,
		test.CountByMethodAndURLRegexp("GET",
			regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/git/ref/(?:refs/)?heads/copier/\d{8}-\d{6}$`)),
		1,
	)
	require.GreaterOrEqual(t,
		test.CountByMethodAndURLRegexp("POST",
			regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/git/trees`)),
		1,
	)
	require.GreaterOrEqual(t,
		test.CountByMethodAndURLRegexp("PATCH",
			regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/git/refs/heads/copier/\d{8}-\d{6}$`)),
		1,
	)
	require.GreaterOrEqual(t,
		test.CountByMethodAndURLRegexp("DELETE",
			regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/git/refs/heads/copier/\d{8}-\d{6}$`)),
		1,
	)
}

func TestAddFiles_DirectConflict_NonFastForward(t *testing.T) {
	_ = test.WithHTTPMock(t)

	owner, repo := test.EnvOwnerRepo(t)
	branch := "main"

	test.SetupOrgToken(owner, "test-token")

	baseRefURL, commitsURL, updateRefURL := test.MockGitHubWriteEndpoints(owner, repo, branch)

	// Override UpdateRef to simulate 422 Unprocessable Entity (non-fast-forward)
	httpmock.RegisterResponder("PATCH", updateRefURL, httpmock.NewJsonResponderOrPanic(422, map[string]any{
		"message": "Update is not a fast forward",
	}))

	files := []github.RepositoryContent{
		{
			Name:    github.Ptr("dir/example1.txt"),
			Path:    github.Ptr("dir/example1.txt"),
			Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("hello 1"))),
		},
	}
	filesToUpload := map[types.UploadKey]types.UploadFileContent{
		{RepoName: repo, BranchPath: "refs/heads/" + branch}: {
			TargetBranch: branch,
			Content:      files,
		},
	}

	services.AddFilesToTargetRepos(context.Background(), test.TestConfig(), filesToUpload, nil, nil)

	info := httpmock.GetCallCountInfo()
	require.Equal(t, 1, info["GET "+baseRefURL])
	require.Equal(t, 1, info["POST "+commitsURL])
	require.Equal(t, 1, info["PATCH "+updateRefURL])
}

func TestAddFiles_ViaPR_MergeConflict_Dirty_NotMerged(t *testing.T) {
	_ = test.WithHTTPMock(t)
	t.Setenv("COPIER_COMMIT_STRATEGY", "pr")

	owner, repo := test.EnvOwnerRepo(t)
	baseBranch := "main"

	cfg := test.TestConfig()
	services.DefaultTokenManager().SetInstallationAccessToken("")
	test.MockGitHubAppTokenEndpoint(cfg.InstallationId)
	err := services.ConfigurePermissions(context.Background(), cfg)
	require.NoError(t, err, "ConfigurePermissions should succeed")

	test.SetupOrgToken(owner, "test-token")

	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/ref/(?:refs/)?heads/`+baseBranch+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/" + baseBranch, "object": map[string]any{"sha": "baseSha"},
		}),
	)
	createRefURL := test.MockCreateRef(owner, repo)

	tempHead := `copier/\d{8}-\d{6}`
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/ref/(?:refs/)?heads/`+tempHead+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/copier/20250101-000000", "object": map[string]any{"sha": "baseSha"},
		}),
	)
	httpmock.RegisterRegexpResponder("DELETE",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/refs/heads/`+tempHead+`$`),
		httpmock.NewStringResponder(204, ""),
	)
	httpmock.RegisterRegexpResponder("POST",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/trees(\?.*)?$`),
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "newTreeSha"}),
	)
	commitsURL := "https://api.github.com/repos/" + owner + "/" + repo + "/git/commits"
	httpmock.RegisterResponder("POST", commitsURL,
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "newCommitSha"}),
	)
	httpmock.RegisterRegexpResponder("PATCH",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/refs/heads/`+tempHead+`$`),
		httpmock.NewStringResponder(200, "{}"),
	)

	pr_number := 77
	httpmock.RegisterResponder("POST",
		"https://api.github.com/repos/"+owner+"/"+repo+"/pulls",
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"number": pr_number, "html_url": "https://github.com/" + owner + "/" + repo + "/pull/77"}),
	)
	httpmock.RegisterResponder("GET",
		"https://api.github.com/repos/"+owner+"/"+repo+"/pulls/77",
		httpmock.NewJsonResponderOrPanic(200, map[string]any{"mergeable": false, "mergeable_state": "dirty"}),
	)

	files := []github.RepositoryContent{{
		Name:    github.Ptr("f.txt"),
		Path:    github.Ptr("f.txt"),
		Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("x"))),
	}}
	filesToUpload := map[types.UploadKey]types.UploadFileContent{
		{RepoName: repo, BranchPath: "refs/heads/" + baseBranch}: {
			TargetBranch:   baseBranch,
			Content:        files,
			CommitStrategy: "pr",
		},
	}

	services.AddFilesToTargetRepos(context.Background(), cfg, filesToUpload, nil, nil)

	info := httpmock.GetCallCountInfo()
	require.Equal(t, 1, info["POST "+createRefURL])
	require.Equal(t, 1, test.CountByMethodAndURLRegexp("POST",
		regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/pulls$`)))
	require.Equal(t, 0, test.CountByMethodAndURLRegexp("PUT",
		regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/pulls/77/merge$`)))
	require.Equal(t, 1, test.CountByMethodAndURLRegexp("DELETE",
		regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/git/refs/heads/copier/\d{8}-\d{6}$`)))
}

func TestPriority_Strategy_ConfigOverridesEnv_And_MessageFallbacks(t *testing.T) {
	_ = test.WithHTTPMock(t)

	owner, repo := test.EnvOwnerRepo(t)
	baseBranch := "main"

	t.Setenv("COPIER_COMMIT_STRATEGY", "pr")

	test.SetupOrgToken(owner, "test-token")

	baseRefURL, commitsURL, updateRefURL := test.MockGitHubWriteEndpoints(owner, repo, baseBranch)

	wantMsg := "Env Default Commit Message"
	testCfg := test.TestConfig()
	testCfg.DefaultCommitMessage = wantMsg

	httpmock.RegisterResponder("POST", commitsURL, func(req *http.Request) (*http.Response, error) {
		defer func() { _ = req.Body.Close() }()
		b, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(b), wantMsg) {
			t.Fatalf("commit body does not contain expected message: %s; body=%s", wantMsg, string(b))
		}
		return httpmock.NewJsonResponse(201, map[string]any{"sha": "newCommitSha"})
	})

	files := []github.RepositoryContent{{
		Name:    github.Ptr("a.txt"),
		Path:    github.Ptr("a.txt"),
		Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("x"))),
	}}

	typeCfg := types.Configs{
		TargetRepo:           repo,
		TargetBranch:         baseBranch,
		CopierCommitStrategy: "direct", // overrides env "pr"
	}

	filesToUpload := map[types.UploadKey]types.UploadFileContent{
		{RepoName: repo, BranchPath: "refs/heads/" + baseBranch, CommitStrategy: typeCfg.CopierCommitStrategy}: {TargetBranch: baseBranch, Content: files},
	}

	services.AddFilesToTargetRepos(context.Background(), testCfg, filesToUpload, nil, nil)

	info := httpmock.GetCallCountInfo()
	require.Equal(t, 1, info["GET "+baseRefURL])
	require.Equal(t, 1, info["POST "+commitsURL])
	require.Equal(t, 1, info["PATCH "+updateRefURL])
	require.Equal(t, 0, test.CountByMethodAndURLRegexp("POST", regexp.MustCompile(`/pulls$`)))
}

func TestPriority_PRTitleDefaultsToCommitMessage_And_NoAutoMergeWhenConfigPresent(t *testing.T) {
	_ = test.WithHTTPMock(t)
	t.Setenv("COPIER_COMMIT_STRATEGY", "pr")

	owner, repo := test.EnvOwnerRepo(t)
	baseBranch := "main"

	cfg := test.TestConfig()
	services.DefaultTokenManager().SetInstallationAccessToken("")
	test.MockGitHubAppTokenEndpoint(cfg.InstallationId)
	err := services.ConfigurePermissions(context.Background(), cfg)
	require.NoError(t, err, "ConfigurePermissions should succeed")

	test.SetupOrgToken(owner, "test-token")

	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/ref/(?:refs/)?heads/`+baseBranch+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{"ref": "refs/heads/" + baseBranch, "object": map[string]any{"sha": "baseSha"}}),
	)
	_ = test.MockCreateRef(owner, repo)
	tempHead := `copier/\d{8}-\d{6}`
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/ref/(?:refs/)?heads/`+tempHead+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{"ref": "refs/heads/copier/20250101-000000", "object": map[string]any{"sha": "baseSha"}}),
	)
	httpmock.RegisterRegexpResponder("DELETE",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/refs/heads/`+tempHead+`$`),
		httpmock.NewStringResponder(204, ""),
	)
	httpmock.RegisterRegexpResponder("POST",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/trees(\?.*)?$`),
		httpmock.NewJsonResponderOrPanic(201, map[string]any{"sha": "t"}),
	)
	commitsURL := "https://api.github.com/repos/" + owner + "/" + repo + "/git/commits"
	want := "Env Fallback Message"
	cfg.DefaultCommitMessage = want
	httpmock.RegisterResponder("POST", commitsURL, func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(b), want) {
			t.Fatalf("expected commit message %q, got body=%s", want, string(b))
		}
		return httpmock.NewJsonResponse(201, map[string]any{"sha": "c"})
	})
	httpmock.RegisterRegexpResponder("PATCH",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/refs/heads/`+tempHead+`$`),
		httpmock.NewStringResponder(200, "{}"),
	)

	httpmock.RegisterResponder("POST",
		"https://api.github.com/repos/"+owner+"/"+repo+"/pulls",
		func(req *http.Request) (*http.Response, error) {
			b, _ := io.ReadAll(req.Body)
			if !strings.Contains(string(b), `"title":"`+want+`"`) {
				t.Fatalf("expected PR title to default to commit message %q; body=%s", want, string(b))
			}
			return httpmock.NewJsonResponse(201, map[string]any{"number": 5})
		},
	)

	files := []github.RepositoryContent{{
		Name: github.Ptr("only.txt"), Path: github.Ptr("only.txt"),
		Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("y"))),
	}}
	filesToUpload := map[types.UploadKey]types.UploadFileContent{
		{RepoName: repo, BranchPath: "refs/heads/" + baseBranch, RuleName: "", CommitStrategy: "pr"}: {TargetBranch: baseBranch, Content: files, CommitStrategy: "pr"},
	}

	services.AddFilesToTargetRepos(context.Background(), cfg, filesToUpload, nil, nil)

	require.Equal(t, 1, test.CountByMethodAndURLRegexp("POST", regexp.MustCompile(`/pulls$`)))
	require.Equal(t, 0, test.CountByMethodAndURLRegexp("PUT", regexp.MustCompile(`/pulls/5/merge$`)))
}

// TestAddFilesToTargetRepos_MixedStrategies_ProducesSeparateOperations verifies
// that two UploadKey entries for the same repo/branch but with different commit
// strategies (direct vs pull_request) produce independent write operations.
func TestAddFilesToTargetRepos_MixedStrategies_ProducesSeparateOperations(t *testing.T) {
	_ = test.WithHTTPMock(t)

	owner, repo := test.EnvOwnerRepo(t)
	baseBranch := "main"

	// Configure token / permissions
	cfg := test.TestConfig()
	services.DefaultTokenManager().SetInstallationAccessToken("")
	test.MockGitHubAppTokenEndpoint(cfg.InstallationId)
	err := services.ConfigurePermissions(context.Background(), cfg)
	require.NoError(t, err, "ConfigurePermissions should succeed")
	test.SetupOrgToken(owner, "test-token")

	// --- Mock direct-commit endpoints ---
	baseRefURL, directCommitsURL, updateRefURL := test.MockGitHubWriteEndpoints(owner, repo, baseBranch)

	// --- Mock PR-strategy endpoints ---
	createRefURL := test.MockCreateRef(owner, repo)
	tempHead := `copier/\d{8}-\d{6}`
	httpmock.RegisterRegexpResponder("GET",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/ref/(?:refs/)?heads/`+tempHead+`$`),
		httpmock.NewJsonResponderOrPanic(200, map[string]any{
			"ref": "refs/heads/copier/20250101-000000", "object": map[string]any{"sha": "baseSha"},
		}),
	)
	httpmock.RegisterRegexpResponder("PATCH",
		regexp.MustCompile(`^https://api\.github\.com/repos/`+owner+`/`+repo+`/git/refs/heads/`+tempHead+`$`),
		httpmock.NewStringResponder(200, "{}"),
	)
	test.MockPullsAndMerge(owner, repo, 99)
	test.MockDeleteTempRef(owner, repo)

	// --- Build two batches for the SAME repo/branch but different strategies ---
	directFiles := []github.RepositoryContent{{
		Name:    github.Ptr("direct-file.txt"),
		Path:    github.Ptr("direct-file.txt"),
		Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("direct content"))),
	}}
	prFiles := []github.RepositoryContent{{
		Name:    github.Ptr("pr-file.txt"),
		Path:    github.Ptr("pr-file.txt"),
		Content: github.Ptr(base64.StdEncoding.EncodeToString([]byte("pr content"))),
	}}

	filesToUpload := map[types.UploadKey]types.UploadFileContent{
		{RepoName: repo, BranchPath: "refs/heads/" + baseBranch, CommitStrategy: "direct"}: {
			TargetBranch:   baseBranch,
			Content:        directFiles,
			CommitStrategy: "direct",
		},
		{RepoName: repo, BranchPath: "refs/heads/" + baseBranch, CommitStrategy: "pull_request"}: {
			TargetBranch:   baseBranch,
			Content:        prFiles,
			CommitStrategy: "pr",
			AutoMergePR:    true,
		},
	}

	services.AddFilesToTargetRepos(context.Background(), cfg, filesToUpload, nil, nil)

	info := httpmock.GetCallCountInfo()

	// Direct-commit path should fire: GET base ref, POST commit, PATCH update ref
	require.GreaterOrEqual(t, info["GET "+baseRefURL], 1, "direct path: GET base ref")
	require.GreaterOrEqual(t, info["POST "+directCommitsURL], 1, "direct path: POST commit")
	require.GreaterOrEqual(t, info["PATCH "+updateRefURL], 1, "direct path: PATCH update ref")

	// PR path should fire: POST create ref (temp branch) + POST pulls
	require.GreaterOrEqual(t, info["POST "+createRefURL], 1, "PR path: POST create temp branch ref")
	require.GreaterOrEqual(t, 1, test.CountByMethodAndURLRegexp("POST",
		regexp.MustCompile(`/repos/`+regexp.QuoteMeta(owner)+`/`+regexp.QuoteMeta(repo)+`/pulls$`),
	), "PR path: POST create PR")
}

func TestDeleteBranchIfExists_NilReference(t *testing.T) {
	_ = test.WithHTTPMock(t)

	cfg := test.TestConfig()
	services.DefaultTokenManager().SetInstallationAccessToken("")
	test.MockGitHubAppTokenEndpoint(cfg.InstallationId)
	err := services.ConfigurePermissions(context.Background(), cfg)
	require.NoError(t, err, "ConfigurePermissions should succeed")

	ctx := context.Background()
	client := services.GetRestClient()

	err = services.DeleteBranchIfExistsExported(ctx, client, cfg.ConfigRepoOwner, "test-org/test-repo", nil)
	require.NoError(t, err, "DeleteBranchIfExistsExported should succeed with nil ref")

	require.Equal(t, 0, test.CountByMethodAndURLRegexp("DELETE", regexp.MustCompile(`/git/refs/`)))
}
