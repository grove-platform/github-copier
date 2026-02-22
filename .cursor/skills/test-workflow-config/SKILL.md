---
name: test-workflow-config
description: Validate and test workflow config YAML files. Use when the user wants to test patterns, transformations, or validate config syntax.
---

# Test Workflow Config

Validate workflow configuration files and test pattern matching using the CLI tools.

## Prerequisites

Build the config-validator tool if not already built:

```bash
go build -o config-validator ./cmd/config-validator
```

## Validation Tasks

### 1. Validate Config File

Validate a workflow config YAML file for syntax and schema errors:

```bash
./config-validator validate -config <path-to-config.yaml> -v
```

Common validation errors:
- Missing required fields (`source.repo`, `destination.repo`)
- Invalid pattern types (must be `prefix`, `glob`, or `regex`)
- Invalid transformation types (must be `move`, `copy`, `glob`, or `regex`)
- Invalid regex syntax in patterns

### 2. Test Pattern Matching

Test if a pattern matches a specific file path:

```bash
./config-validator test-pattern \
  -type <prefix|glob|regex> \
  -pattern "<pattern>" \
  -file "<file-path>"
```

**Examples:**

```bash
# Glob pattern
./config-validator test-pattern -type glob -pattern "examples/**/*.go" -file "examples/auth/main.go"

# Regex with capture groups
./config-validator test-pattern -type regex -pattern "^examples/(?P<lang>[^/]+)/(?P<file>.+)$" -file "examples/go/main.go"

# Prefix pattern
./config-validator test-pattern -type prefix -pattern "docs/" -file "docs/api/reference.md"
```

### 3. Test Path Transformation

Test how a file path transforms with a given template:

```bash
./config-validator test-transform \
  -template "<transform-template>" \
  -file "<source-file-path>" \
  -pattern "<regex-pattern>"
```

**Examples:**

```bash
# Move transformation
./config-validator test-transform \
  -template "code/${lang}/${file}" \
  -file "examples/go/main.go" \
  -pattern "^examples/(?P<lang>[^/]+)/(?P<file>.+)$"
# Output: code/go/main.go

# Using built-in variables
./config-validator test-transform \
  -template "archive/${filename}" \
  -file "src/utils/helper.go" \
  -pattern ".*"
# Output: archive/helper.go
```

**Built-in variables:**
- `${path}` - Full source path
- `${filename}` - File name only
- `${dir}` - Directory path
- `${ext}` - File extension
- Custom: Any named groups from regex (`(?P<name>...)`)

### 4. Initialize New Config

Generate a starter config from a template:

```bash
./config-validator init -output copier-config.yaml
```

## Troubleshooting

If validation passes but files aren't being copied:

1. Check that the source repo has the GitHub App installed
2. Verify the webhook is being received (`/health` endpoint shows recent deliveries)
3. Test patterns against actual file paths from the PR
4. Check logs for "no matching files" messages

## Related Files

- Config schema: `types/config.go`
- Pattern matching logic: `services/pattern_matcher.go`
- Workflow processing: `services/workflow_processor.go`
- Config loader: `services/config_loader.go`, `services/main_config_loader.go`
