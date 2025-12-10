# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- CI/CD pipeline with GitHub Actions (`.github/workflows/ci.yml`)
  - Test job with race detection
  - Lint job with golangci-lint
  - Security scanning with gosec
  - Build verification
- Pre-commit hooks for secrets detection and Go linting (`.pre-commit-config.yaml`)
- AGENT.md for AI agent context
- Comprehensive test suite for `workflow_processor.go` (843 lines, 94%+ coverage)
- Integration test harness for local testing (`scripts/integration-test.sh`)
- Test environment configuration (`test-payloads/.env.test`)

### Changed
- **BREAKING**: Renamed module from `github.com/mongodb/code-example-tooling/code-copier` to `github.com/grove-platform/github-copier`
- Renamed binary from `examples-copier` to `github-copier`
- All `log.Fatal` calls replaced with proper error returns for graceful error handling
- `FileStateService.filesToDeprecate` changed from single-entry map to slice-based accumulation

### Fixed
- Deprecation file accumulation bug: multiple deprecated files now correctly accumulate instead of overwriting
- Nil pointer dereference bugs across GitHub API calls in:
  - `services/github_read.go`
  - `services/github_write_to_source.go`
  - `services/main_config_loader.go`
  - `services/config_loader.go`

### Security
- Added gitleaks pre-commit hook for secrets detection
- Added gosec security scanning in CI pipeline

## [1.0.0] - 2024-12-01

### Added
- Initial release after migration from `mongodb/code-example-tooling` monorepo
- Webhook service for automated file copying on PR merge
- Pattern matching support: prefix, glob, regex
- Transformation types: move, copy, glob, regex
- Main config system with `$ref` support for distributed workflow configs
- Commit strategies: direct commit or pull request
- Health and metrics endpoints
- Slack notifications for operational visibility
- MongoDB audit logging (optional)
- Google Cloud Logging integration
- Dry-run mode for testing

[Unreleased]: https://github.com/grove-platform/github-copier/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/grove-platform/github-copier/releases/tag/v1.0.0

