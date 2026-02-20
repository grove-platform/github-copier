package services_test

import (
	"sync"
	"testing"

	"github.com/google/go-github/v82/github"
	"github.com/grove-platform/github-copier/services"
	"github.com/grove-platform/github-copier/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileStateService_AddAndGetFilesToUpload(t *testing.T) {
	service := services.NewFileStateService()

	key := types.UploadKey{
		RepoName:       "org/repo",
		BranchPath:     "refs/heads/main",
		RuleName:       "test-rule",
		CommitStrategy: "direct",
	}

	content := types.UploadFileContent{
		TargetBranch:   "main",
		CommitStrategy: types.CommitStrategyDirect,
		CommitMessage:  "Test commit",
		Content: []github.RepositoryContent{
			{Path: github.Ptr("test.go")},
		},
	}

	// Add file
	service.AddFileToUpload(key, content)

	// Get files
	files := service.GetFilesToUpload()
	require.Len(t, files, 1)

	retrieved, exists := files[key]
	require.True(t, exists)
	assert.Equal(t, "main", retrieved.TargetBranch)
	assert.Equal(t, types.CommitStrategyDirect, retrieved.CommitStrategy)
	assert.Equal(t, "Test commit", retrieved.CommitMessage)
	assert.Len(t, retrieved.Content, 1)
}

func TestFileStateService_AddAndGetFilesToDeprecate(t *testing.T) {
	service := services.NewFileStateService()

	entry := types.DeprecatedFileEntry{
		FileName: "old_example.go",
		Repo:     "org/repo",
		Branch:   "main",
	}

	// Add file
	service.AddFileToDeprecate("deprecated.json", entry)

	// Get files
	files := service.GetFilesToDeprecate()
	require.Len(t, files, 1)

	entries, exists := files["deprecated.json"]
	require.True(t, exists)
	require.Len(t, entries, 1)
	assert.Equal(t, "old_example.go", entries[0].FileName)
	assert.Equal(t, "org/repo", entries[0].Repo)
	assert.Equal(t, "main", entries[0].Branch)
}

func TestFileStateService_MultipleDeprecatedFilesAccumulate(t *testing.T) {
	service := services.NewFileStateService()

	// Add multiple files to the same deprecation file
	entry1 := types.DeprecatedFileEntry{
		FileName: "file1.go",
		Repo:     "org/repo",
		Branch:   "main",
	}
	entry2 := types.DeprecatedFileEntry{
		FileName: "file2.go",
		Repo:     "org/repo",
		Branch:   "main",
	}
	entry3 := types.DeprecatedFileEntry{
		FileName: "file3.go",
		Repo:     "org/repo",
		Branch:   "main",
	}

	service.AddFileToDeprecate("deprecated.json", entry1)
	service.AddFileToDeprecate("deprecated.json", entry2)
	service.AddFileToDeprecate("deprecated.json", entry3)

	// Get files - should have all 3 entries
	files := service.GetFilesToDeprecate()
	require.Len(t, files, 1) // One deprecation file

	entries, exists := files["deprecated.json"]
	require.True(t, exists)
	require.Len(t, entries, 3) // Three entries accumulated

	// Verify all entries are present
	fileNames := make([]string, len(entries))
	for i, e := range entries {
		fileNames[i] = e.FileName
	}
	assert.Contains(t, fileNames, "file1.go")
	assert.Contains(t, fileNames, "file2.go")
	assert.Contains(t, fileNames, "file3.go")
}

func TestFileStateService_MultipleDeprecationFiles(t *testing.T) {
	service := services.NewFileStateService()

	// Add entries to different deprecation files
	entry1 := types.DeprecatedFileEntry{
		FileName: "file1.go",
		Repo:     "org/repo1",
		Branch:   "main",
	}
	entry2 := types.DeprecatedFileEntry{
		FileName: "file2.go",
		Repo:     "org/repo2",
		Branch:   "develop",
	}

	service.AddFileToDeprecate("deprecated_repo1.json", entry1)
	service.AddFileToDeprecate("deprecated_repo2.json", entry2)

	// Get files - should have 2 deprecation files
	files := service.GetFilesToDeprecate()
	require.Len(t, files, 2)

	entries1, exists := files["deprecated_repo1.json"]
	require.True(t, exists)
	require.Len(t, entries1, 1)
	assert.Equal(t, "file1.go", entries1[0].FileName)

	entries2, exists := files["deprecated_repo2.json"]
	require.True(t, exists)
	require.Len(t, entries2, 1)
	assert.Equal(t, "file2.go", entries2[0].FileName)
}

func TestFileStateService_ClearFilesToUpload(t *testing.T) {
	service := services.NewFileStateService()

	key := types.UploadKey{
		RepoName:       "org/repo",
		BranchPath:     "refs/heads/main",
		RuleName:       "test-rule",
		CommitStrategy: "direct",
	}

	content := types.UploadFileContent{
		TargetBranch: "main",
	}

	service.AddFileToUpload(key, content)
	assert.Len(t, service.GetFilesToUpload(), 1)

	service.ClearFilesToUpload()
	assert.Len(t, service.GetFilesToUpload(), 0)
}

func TestFileStateService_ClearFilesToDeprecate(t *testing.T) {
	service := services.NewFileStateService()

	entry := types.DeprecatedFileEntry{
		FileName: "test.go",
		Repo:     "org/repo",
		Branch:   "main",
	}

	service.AddFileToDeprecate("deprecated.json", entry)
	assert.Len(t, service.GetFilesToDeprecate(), 1)

	service.ClearFilesToDeprecate()
	assert.Len(t, service.GetFilesToDeprecate(), 0)
}

func TestFileStateService_UpdateExistingFile(t *testing.T) {
	service := services.NewFileStateService()

	key := types.UploadKey{
		RepoName:   "org/repo",
		BranchPath: "refs/heads/main",
	}

	// Add first file
	content1 := types.UploadFileContent{
		TargetBranch: "main",
		Content: []github.RepositoryContent{
			{Path: github.Ptr("file1.go")},
		},
	}
	service.AddFileToUpload(key, content1)

	// Update with second file
	content2 := types.UploadFileContent{
		TargetBranch: "main",
		Content: []github.RepositoryContent{
			{Path: github.Ptr("file1.go")},
			{Path: github.Ptr("file2.go")},
		},
	}
	service.AddFileToUpload(key, content2)

	// Should have replaced, not appended
	files := service.GetFilesToUpload()
	require.Len(t, files, 1)
	assert.Len(t, files[key].Content, 2)
}

func TestFileStateService_ThreadSafety(t *testing.T) {
	service := services.NewFileStateService()

	var wg sync.WaitGroup
	numGoroutines := 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			key := types.UploadKey{
				RepoName:   "org/repo",
				BranchPath: "refs/heads/main",
			}

			content := types.UploadFileContent{
				TargetBranch: "main",
			}

			service.AddFileToUpload(key, content)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = service.GetFilesToUpload()
		}()
	}

	wg.Wait()

	// Should have one entry (all goroutines wrote to same key)
	files := service.GetFilesToUpload()
	assert.Len(t, files, 1)
}

func TestFileStateService_MultipleRepos(t *testing.T) {
	service := services.NewFileStateService()

	key1 := types.UploadKey{
		RepoName:   "org/repo1",
		BranchPath: "refs/heads/main",
	}

	key2 := types.UploadKey{
		RepoName:   "org/repo2",
		BranchPath: "refs/heads/develop",
	}

	content1 := types.UploadFileContent{
		TargetBranch: "main",
		Content: []github.RepositoryContent{
			{Path: github.Ptr("file1.go")},
		},
	}

	content2 := types.UploadFileContent{
		TargetBranch: "develop",
		Content: []github.RepositoryContent{
			{Path: github.Ptr("file2.go")},
		},
	}

	service.AddFileToUpload(key1, content1)
	service.AddFileToUpload(key2, content2)

	files := service.GetFilesToUpload()
	require.Len(t, files, 2)

	assert.Equal(t, "main", files[key1].TargetBranch)
	assert.Equal(t, "develop", files[key2].TargetBranch)
}

func TestFileStateService_IsolatedCopies(t *testing.T) {
	service := services.NewFileStateService()

	key := types.UploadKey{
		RepoName:       "org/repo",
		BranchPath:     "refs/heads/main",
		RuleName:       "test-rule",
		CommitStrategy: "direct",
	}

	content := types.UploadFileContent{
		TargetBranch: "main",
		Content: []github.RepositoryContent{
			{Path: github.Ptr("file1.go")},
		},
	}

	service.AddFileToUpload(key, content)

	// Get first copy
	files1 := service.GetFilesToUpload()

	// Get second copy
	files2 := service.GetFilesToUpload()

	// Modify first copy (should not affect second)
	for k := range files1 {
		delete(files1, k)
	}

	// Second copy should still have the data
	assert.Len(t, files2, 1)

	// Original service should still have the data
	assert.Len(t, service.GetFilesToUpload(), 1)
}

func TestFileStateService_CommitStrategyTypes(t *testing.T) {
	service := services.NewFileStateService()

	tests := []struct {
		name     string
		strategy types.CommitStrategy
	}{
		{
			name:     "direct commit",
			strategy: types.CommitStrategyDirect,
		},
		{
			name:     "pull request",
			strategy: types.CommitStrategyPR,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := types.UploadKey{
				RepoName:       "org/repo",
				BranchPath:     "refs/heads/main",
				RuleName:       "test-rule",
				CommitStrategy: string(tt.strategy),
			}

			content := types.UploadFileContent{
				TargetBranch:   "main",
				CommitStrategy: tt.strategy,
				CommitMessage:  "Test",
				PRTitle:        "Test PR",
				AutoMergePR:    i%2 == 0,
			}

			service.AddFileToUpload(key, content)

			files := service.GetFilesToUpload()
			retrieved := files[key]

			assert.Equal(t, tt.strategy, retrieved.CommitStrategy)
			assert.Equal(t, "Test", retrieved.CommitMessage)
			assert.Equal(t, "Test PR", retrieved.PRTitle)
			assert.Equal(t, i%2 == 0, retrieved.AutoMergePR)

			service.ClearFilesToUpload()
		})
	}
}
