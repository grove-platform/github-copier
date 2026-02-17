package services_test

import (
	"context"
	"testing"

	"github.com/grove-platform/github-copier/services"
	"github.com/grove-platform/github-copier/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	test "github.com/grove-platform/github-copier/tests"
)

// ============================================================================
// Mock implementations
// ============================================================================

// mockMessageTemplater is a mock implementation of MessageTemplater
type mockMessageTemplater struct{}

func (m *mockMessageTemplater) RenderCommitMessage(template string, ctx *types.MessageContext) string {
	if template == "" {
		return "Default commit message"
	}
	return template
}

func (m *mockMessageTemplater) RenderPRTitle(template string, ctx *types.MessageContext) string {
	if template == "" {
		return "Default PR title"
	}
	return template
}

func (m *mockMessageTemplater) RenderPRBody(template string, ctx *types.MessageContext) string {
	if template == "" {
		return "Default PR body"
	}
	return template
}

// ============================================================================
// Test helper functions
// ============================================================================

func createTestWorkflow(name string, transformations []types.Transformation) types.Workflow {
	return types.Workflow{
		Name: name,
		Source: types.Source{
			Repo:   "test-org/source-repo",
			Branch: "main",
		},
		Destination: types.Destination{
			Repo:   "test-org/dest-repo",
			Branch: "main",
		},
		Transformations: transformations,
	}
}

func createMoveTransformation(from, to string) types.Transformation {
	return types.Transformation{
		Move: &types.MoveTransform{From: from, To: to},
	}
}

func createCopyTransformation(from, to string) types.Transformation {
	return types.Transformation{
		Copy: &types.CopyTransform{From: from, To: to},
	}
}

func createGlobTransformation(pattern, transform string) types.Transformation {
	return types.Transformation{
		Glob: &types.GlobTransform{Pattern: pattern, Transform: transform},
	}
}

func createRegexTransformation(pattern, transform string) types.Transformation {
	return types.Transformation{
		Regex: &types.RegexTransform{Pattern: pattern, Transform: transform},
	}
}

// ============================================================================
// Tests for Move Transformation
// ============================================================================

func TestWorkflowProcessor_MoveTransformation(t *testing.T) {
	tests := []struct {
		name       string
		from       string
		to         string
		sourcePath string
		wantMatch  bool
		wantTarget string
	}{
		{
			name:       "simple directory move",
			from:       "src",
			to:         "dest",
			sourcePath: "src/file.go",
			wantMatch:  true,
			wantTarget: "dest/file.go",
		},
		{
			name:       "nested directory move",
			from:       "examples/go",
			to:         "code/golang",
			sourcePath: "examples/go/main.go",
			wantMatch:  true,
			wantTarget: "code/golang/main.go",
		},
		{
			name:       "deeply nested path",
			from:       "src",
			to:         "dest",
			sourcePath: "src/pkg/internal/utils/helper.go",
			wantMatch:  true,
			wantTarget: "dest/pkg/internal/utils/helper.go",
		},
		{
			name:       "exact file match",
			from:       "README.md",
			to:         "docs/README.md",
			sourcePath: "README.md",
			wantMatch:  true,
			wantTarget: "docs/README.md",
		},
		{
			name:       "no match - different prefix",
			from:       "src",
			to:         "dest",
			sourcePath: "other/file.go",
			wantMatch:  false,
			wantTarget: "",
		},
		{
			name:       "no match - partial prefix",
			from:       "src",
			to:         "dest",
			sourcePath: "srcfile.go",
			wantMatch:  false,
			wantTarget: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create processor with real implementations for move (doesn't need mocks)
			fileStateService := services.NewFileStateService()
			processor := services.NewWorkflowProcessor(
				services.NewPatternMatcher(),
				services.NewPathTransformer(),
				fileStateService,
				nil, // metrics collector
				&mockMessageTemplater{},
				test.TestConfig(),
			)

			workflow := createTestWorkflow("test-workflow", []types.Transformation{
				createMoveTransformation(tt.from, tt.to),
			})

			// Create a removed file to avoid GitHub API calls
			changedFiles := []types.ChangedFile{
				{Path: tt.sourcePath, Status: "removed"},
			}

			err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
			require.NoError(t, err)

			// Check deprecation map for removed files
			deprecated := fileStateService.GetFilesToDeprecate()
			if tt.wantMatch {
				assert.NotEmpty(t, deprecated, "expected file to be added to deprecation map")
			} else {
				assert.Empty(t, deprecated, "expected no files in deprecation map")
			}
		})
	}
}

// ============================================================================
// Tests for Copy Transformation
// ============================================================================

func TestWorkflowProcessor_CopyTransformation(t *testing.T) {
	tests := []struct {
		name       string
		from       string
		to         string
		sourcePath string
		wantMatch  bool
	}{
		{
			name:       "exact file copy",
			from:       "config.yaml",
			to:         "settings/config.yaml",
			sourcePath: "config.yaml",
			wantMatch:  true,
		},
		{
			name:       "nested file copy",
			from:       "src/main.go",
			to:         "app/main.go",
			sourcePath: "src/main.go",
			wantMatch:  true,
		},
		{
			name:       "no match - different file",
			from:       "config.yaml",
			to:         "settings/config.yaml",
			sourcePath: "other.yaml",
			wantMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileStateService := services.NewFileStateService()
			processor := services.NewWorkflowProcessor(
				services.NewPatternMatcher(),
				services.NewPathTransformer(),
				fileStateService,
				nil,
				&mockMessageTemplater{},
				test.TestConfig(),
			)

			workflow := createTestWorkflow("test-workflow", []types.Transformation{
				createCopyTransformation(tt.from, tt.to),
			})

			changedFiles := []types.ChangedFile{
				{Path: tt.sourcePath, Status: "removed"},
			}

			err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
			require.NoError(t, err)

			deprecated := fileStateService.GetFilesToDeprecate()
			if tt.wantMatch {
				assert.NotEmpty(t, deprecated, "expected file to be added to deprecation map")
			} else {
				assert.Empty(t, deprecated, "expected no files in deprecation map")
			}
		})
	}
}

// ============================================================================
// Tests for Glob Transformation
// ============================================================================

func TestWorkflowProcessor_GlobTransformation(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		transform  string
		sourcePath string
		wantMatch  bool
	}{
		{
			name:       "simple glob pattern",
			pattern:    "src/**/*.go",
			transform:  "dest/${relative_path}",
			sourcePath: "src/main.go",
			wantMatch:  true,
		},
		{
			name:       "nested glob pattern",
			pattern:    "examples/**/*.js",
			transform:  "code/${relative_path}",
			sourcePath: "examples/app/index.js",
			wantMatch:  true,
		},
		{
			name:       "no match - wrong extension",
			pattern:    "src/**/*.go",
			transform:  "dest/${relative_path}",
			sourcePath: "src/main.py",
			wantMatch:  false,
		},
		{
			name:       "no match - wrong directory",
			pattern:    "src/**/*.go",
			transform:  "dest/${relative_path}",
			sourcePath: "other/main.go",
			wantMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileStateService := services.NewFileStateService()
			processor := services.NewWorkflowProcessor(
				services.NewPatternMatcher(),
				services.NewPathTransformer(),
				fileStateService,
				nil,
				&mockMessageTemplater{},
				test.TestConfig(),
			)

			workflow := createTestWorkflow("test-workflow", []types.Transformation{
				createGlobTransformation(tt.pattern, tt.transform),
			})

			changedFiles := []types.ChangedFile{
				{Path: tt.sourcePath, Status: "removed"},
			}

			err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
			require.NoError(t, err)

			deprecated := fileStateService.GetFilesToDeprecate()
			if tt.wantMatch {
				assert.NotEmpty(t, deprecated, "expected file to be added to deprecation map")
			} else {
				assert.Empty(t, deprecated, "expected no files in deprecation map")
			}
		})
	}
}

// ============================================================================
// Tests for Regex Transformation
// ============================================================================

func TestWorkflowProcessor_RegexTransformation(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		transform  string
		sourcePath string
		wantMatch  bool
	}{
		{
			name:       "simple regex pattern",
			pattern:    `^src/(?P<file>.+)\.go$`,
			transform:  "dest/${file}.go",
			sourcePath: "src/main.go",
			wantMatch:  true,
		},
		{
			name:       "regex with multiple groups",
			pattern:    `^examples/(?P<lang>[^/]+)/(?P<file>.+)$`,
			transform:  "code/${lang}/${file}",
			sourcePath: "examples/go/main.go",
			wantMatch:  true,
		},
		{
			name:       "no match - pattern doesn't match",
			pattern:    `^src/(?P<file>.+)\.go$`,
			transform:  "dest/${file}.go",
			sourcePath: "other/main.go",
			wantMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileStateService := services.NewFileStateService()
			processor := services.NewWorkflowProcessor(
				services.NewPatternMatcher(),
				services.NewPathTransformer(),
				fileStateService,
				nil,
				&mockMessageTemplater{},
				test.TestConfig(),
			)

			workflow := createTestWorkflow("test-workflow", []types.Transformation{
				createRegexTransformation(tt.pattern, tt.transform),
			})

			changedFiles := []types.ChangedFile{
				{Path: tt.sourcePath, Status: "removed"},
			}

			err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
			require.NoError(t, err)

			deprecated := fileStateService.GetFilesToDeprecate()
			if tt.wantMatch {
				assert.NotEmpty(t, deprecated, "expected file to be added to deprecation map")
			} else {
				assert.Empty(t, deprecated, "expected no files in deprecation map")
			}
		})
	}
}

// ============================================================================
// Tests for Exclude Patterns
// ============================================================================

func TestWorkflowProcessor_ExcludePatterns(t *testing.T) {
	// Note: Exclude patterns use regex matching (consistent with documentation and SourcePattern.ExcludePatterns)
	tests := []struct {
		name         string
		exclude      []string
		sourcePath   string
		wantExcluded bool
	}{
		{
			name:         "exclude by extension regex",
			exclude:      []string{".*_test\\.go$"},
			sourcePath:   "src/main_test.go",
			wantExcluded: true,
		},
		{
			name:         "exclude by directory regex",
			exclude:      []string{"^vendor/"},
			sourcePath:   "vendor/pkg/lib.go",
			wantExcluded: true,
		},
		{
			name:         "exclude by filename regex",
			exclude:      []string{"\\.DS_Store$"},
			sourcePath:   "src/.DS_Store",
			wantExcluded: true,
		},
		{
			name:         "not excluded - no match",
			exclude:      []string{".*_test\\.go$"},
			sourcePath:   "src/main.go",
			wantExcluded: false,
		},
		{
			name:         "multiple exclude patterns - first matches",
			exclude:      []string{".*_test\\.go$", "^vendor/"},
			sourcePath:   "src/main_test.go",
			wantExcluded: true,
		},
		{
			name:         "multiple exclude patterns - second matches",
			exclude:      []string{".*_test\\.go$", "^vendor/"},
			sourcePath:   "vendor/lib.go",
			wantExcluded: true,
		},
		{
			name:         "multiple exclude patterns - none match",
			exclude:      []string{".*_test\\.go$", "^vendor/"},
			sourcePath:   "src/main.go",
			wantExcluded: false,
		},
		{
			name:         "exclude spec files",
			exclude:      []string{".*\\.spec\\.ts$"},
			sourcePath:   "src/utils/logger.spec.ts",
			wantExcluded: true,
		},
		{
			name:         "exclude eslint config",
			exclude:      []string{"\\.eslintrc"},
			sourcePath:   "examples/typescript/.eslintrc.json",
			wantExcluded: true,
		},
		{
			name:         "exclude node_modules directory",
			exclude:      []string{"node_modules/"},
			sourcePath:   "examples/typescript/node_modules/pkg/index.js",
			wantExcluded: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileStateService := services.NewFileStateService()
			processor := services.NewWorkflowProcessor(
				services.NewPatternMatcher(),
				services.NewPathTransformer(),
				fileStateService,
				nil,
				&mockMessageTemplater{},
				test.TestConfig(),
			)

			workflow := types.Workflow{
				Name: "test-workflow",
				Source: types.Source{
					Repo:   "test-org/source-repo",
					Branch: "main",
				},
				Destination: types.Destination{
					Repo:   "test-org/dest-repo",
					Branch: "main",
				},
				Transformations: []types.Transformation{
					createMoveTransformation("src", "dest"),
					createMoveTransformation("vendor", "vendor"),
				},
				Exclude: tt.exclude,
			}

			changedFiles := []types.ChangedFile{
				{Path: tt.sourcePath, Status: "removed"},
			}

			err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
			require.NoError(t, err)

			deprecated := fileStateService.GetFilesToDeprecate()
			if tt.wantExcluded {
				assert.Empty(t, deprecated, "expected file to be excluded")
			} else {
				// Only check if the path matches a transformation
				if tt.sourcePath == "src/main.go" {
					assert.NotEmpty(t, deprecated, "expected file to be processed")
				}
			}
		})
	}
}

// ============================================================================
// Tests for Multiple Transformations
// ============================================================================

func TestWorkflowProcessor_MultipleTransformations(t *testing.T) {
	fileStateService := services.NewFileStateService()
	processor := services.NewWorkflowProcessor(
		services.NewPatternMatcher(),
		services.NewPathTransformer(),
		fileStateService,
		nil,
		&mockMessageTemplater{},
		test.TestConfig(),
	)

	workflow := types.Workflow{
		Name: "multi-transform-workflow",
		Source: types.Source{
			Repo:   "test-org/source-repo",
			Branch: "main",
		},
		Destination: types.Destination{
			Repo:   "test-org/dest-repo",
			Branch: "main",
		},
		Transformations: []types.Transformation{
			createMoveTransformation("src", "code"),
			createMoveTransformation("docs", "documentation"),
			createCopyTransformation("README.md", "docs/README.md"),
		},
	}

	changedFiles := []types.ChangedFile{
		{Path: "src/main.go", Status: "removed"},
		{Path: "docs/guide.md", Status: "removed"},
		{Path: "README.md", Status: "removed"},
		{Path: "other/file.txt", Status: "removed"}, // Should not match
	}

	err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
	require.NoError(t, err)

	deprecated := fileStateService.GetFilesToDeprecate()
	assert.NotEmpty(t, deprecated, "expected files to be processed")

	// Verify all 3 matching files are accumulated in the deprecation map
	entries, exists := deprecated["deprecated_examples.json"]
	assert.True(t, exists, "expected deprecation entry to exist")
	require.Len(t, entries, 3, "expected 3 files to be accumulated (src/main.go, docs/guide.md, README.md)")

	// Collect all file names
	fileNames := make([]string, len(entries))
	for i, e := range entries {
		fileNames[i] = e.FileName
	}

	// Verify all expected files are present
	assert.Contains(t, fileNames, "code/main.go", "expected src/main.go -> code/main.go")
	assert.Contains(t, fileNames, "documentation/guide.md", "expected docs/guide.md -> documentation/guide.md")
	assert.Contains(t, fileNames, "docs/README.md", "expected README.md -> docs/README.md")
}

// ============================================================================
// Tests for Edge Cases
// ============================================================================

func TestWorkflowProcessor_EmptyChangedFiles(t *testing.T) {
	fileStateService := services.NewFileStateService()
	processor := services.NewWorkflowProcessor(
		services.NewPatternMatcher(),
		services.NewPathTransformer(),
		fileStateService,
		nil,
		&mockMessageTemplater{},
		test.TestConfig(),
	)

	workflow := createTestWorkflow("test-workflow", []types.Transformation{
		createMoveTransformation("src", "dest"),
	})

	changedFiles := []types.ChangedFile{}

	err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
	require.NoError(t, err)

	deprecated := fileStateService.GetFilesToDeprecate()
	assert.Empty(t, deprecated, "expected no files to be processed")
}

func TestWorkflowProcessor_NoTransformations(t *testing.T) {
	fileStateService := services.NewFileStateService()
	processor := services.NewWorkflowProcessor(
		services.NewPatternMatcher(),
		services.NewPathTransformer(),
		fileStateService,
		nil,
		&mockMessageTemplater{},
		test.TestConfig(),
	)

	workflow := createTestWorkflow("test-workflow", []types.Transformation{})

	changedFiles := []types.ChangedFile{
		{Path: "src/main.go", Status: "removed"},
	}

	err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
	require.NoError(t, err)

	deprecated := fileStateService.GetFilesToDeprecate()
	assert.Empty(t, deprecated, "expected no files to be processed with no transformations")
}

// ============================================================================
// Tests for Invalid Patterns
// ============================================================================

func TestWorkflowProcessor_InvalidExcludePattern(t *testing.T) {
	fileStateService := services.NewFileStateService()
	processor := services.NewWorkflowProcessor(
		services.NewPatternMatcher(),
		services.NewPathTransformer(),
		fileStateService,
		nil,
		&mockMessageTemplater{},
		test.TestConfig(),
	)

	workflow := types.Workflow{
		Name: "test-workflow",
		Source: types.Source{
			Repo:   "test-org/source-repo",
			Branch: "main",
		},
		Destination: types.Destination{
			Repo:   "test-org/dest-repo",
			Branch: "main",
		},
		Transformations: []types.Transformation{
			createMoveTransformation("src", "dest"),
		},
		// Invalid glob pattern - should be handled gracefully
		Exclude: []string{"[invalid"},
	}

	changedFiles := []types.ChangedFile{
		{Path: "src/main.go", Status: "removed"},
	}

	// Should not error, just log warning and continue
	err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
	require.NoError(t, err)

	// File should still be processed since invalid pattern is skipped
	deprecated := fileStateService.GetFilesToDeprecate()
	assert.NotEmpty(t, deprecated, "expected file to be processed despite invalid exclude pattern")
}

// ============================================================================
// Tests for Deprecation Config
// ============================================================================

func TestWorkflowProcessor_CustomDeprecationFile(t *testing.T) {
	fileStateService := services.NewFileStateService()
	processor := services.NewWorkflowProcessor(
		services.NewPatternMatcher(),
		services.NewPathTransformer(),
		fileStateService,
		nil,
		&mockMessageTemplater{},
		test.TestConfig(),
	)

	workflow := types.Workflow{
		Name: "test-workflow",
		Source: types.Source{
			Repo:   "test-org/source-repo",
			Branch: "main",
		},
		Destination: types.Destination{
			Repo:   "test-org/dest-repo",
			Branch: "main",
		},
		Transformations: []types.Transformation{
			createMoveTransformation("src", "dest"),
		},
		DeprecationCheck: &types.DeprecationConfig{
			File: "custom_deprecation.json",
		},
	}

	changedFiles := []types.ChangedFile{
		{Path: "src/main.go", Status: "removed"},
	}

	err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
	require.NoError(t, err)

	deprecated := fileStateService.GetFilesToDeprecate()
	entries, exists := deprecated["custom_deprecation.json"]
	assert.True(t, exists, "expected custom deprecation file to be used")
	require.Len(t, entries, 1, "expected one entry in custom deprecation file")
	assert.Equal(t, "dest/main.go", entries[0].FileName)
}

// ============================================================================
// Tests for File Status Handling
// ============================================================================

func TestWorkflowProcessor_FileStatusHandling(t *testing.T) {
	tests := []struct {
		name             string
		status           string
		expectDeprecated bool
	}{
		{
			name:             "removed file goes to deprecation",
			status:           "removed",
			expectDeprecated: true,
		},
		{
			name:             "DELETED file goes to deprecation (GraphQL API format)",
			status:           "DELETED",
			expectDeprecated: true,
		},
		{
			name:             "added file does not go to deprecation",
			status:           "added",
			expectDeprecated: false,
		},
		{
			name:             "modified file does not go to deprecation",
			status:           "modified",
			expectDeprecated: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileStateService := services.NewFileStateService()
			processor := services.NewWorkflowProcessor(
				services.NewPatternMatcher(),
				services.NewPathTransformer(),
				fileStateService,
				nil,
				&mockMessageTemplater{},
				test.TestConfig(),
			)

			workflow := createTestWorkflow("test-workflow", []types.Transformation{
				createMoveTransformation("src", "dest"),
			})

			changedFiles := []types.ChangedFile{
				{Path: "src/main.go", Status: tt.status},
			}

			// Note: For non-removed files, this will try to call GitHub API
			// which will fail, but we're testing the deprecation path
			_ = processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")

			deprecated := fileStateService.GetFilesToDeprecate()
			if tt.expectDeprecated {
				assert.NotEmpty(t, deprecated, "expected file to be in deprecation map")
			} else {
				// For non-removed files, they go to upload queue (which fails without GitHub)
				// but should NOT be in deprecation map
				assert.Empty(t, deprecated, "expected file NOT to be in deprecation map")
			}
		})
	}
}

// ============================================================================
// Tests for Path Transformation Edge Cases
// ============================================================================

func TestWorkflowProcessor_PathTransformationEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		transform  types.Transformation
		sourcePath string
		wantMatch  bool
	}{
		{
			name:       "move with trailing slash in from",
			transform:  createMoveTransformation("src/", "dest/"),
			sourcePath: "src/main.go",
			wantMatch:  true,
		},
		{
			name:       "move empty from does not match root file",
			transform:  createMoveTransformation("", "dest"),
			sourcePath: "main.go",
			wantMatch:  false, // Empty prefix doesn't match in current implementation
		},
		{
			name:       "glob single star does not match nested",
			transform:  createGlobTransformation("src/*.go", "dest/${relative_path}"),
			sourcePath: "src/pkg/main.go",
			wantMatch:  false,
		},
		{
			name:       "copy exact file match",
			transform:  createCopyTransformation("README.md", "docs/README.md"),
			sourcePath: "README.md",
			wantMatch:  true,
		},
		{
			name:       "copy does not match different file",
			transform:  createCopyTransformation("README.md", "docs/README.md"),
			sourcePath: "CHANGELOG.md",
			wantMatch:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileStateService := services.NewFileStateService()
			processor := services.NewWorkflowProcessor(
				services.NewPatternMatcher(),
				services.NewPathTransformer(),
				fileStateService,
				nil,
				&mockMessageTemplater{},
				test.TestConfig(),
			)

			workflow := createTestWorkflow("test-workflow", []types.Transformation{tt.transform})

			changedFiles := []types.ChangedFile{
				{Path: tt.sourcePath, Status: "removed"},
			}

			err := processor.ProcessWorkflow(context.Background(), workflow, changedFiles, 1, "abc123")
			require.NoError(t, err)

			deprecated := fileStateService.GetFilesToDeprecate()
			if tt.wantMatch {
				assert.NotEmpty(t, deprecated, "expected file to match transformation")
			} else {
				assert.Empty(t, deprecated, "expected file NOT to match transformation")
			}
		})
	}
}
