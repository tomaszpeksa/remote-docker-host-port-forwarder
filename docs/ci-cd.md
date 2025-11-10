# CI/CD Pipeline Documentation

This document describes the continuous integration and continuous delivery (CI/CD) infrastructure for the Remote Docker Host Port Forwarder project.

## Overview

The project uses GitHub Actions for automated testing, code quality checks, and releases. The CI/CD pipeline ensures code quality, catches bugs early, and streamlines the release process.

## Workflows

### 1. CI Workflow (`.github/workflows/ci.yml`)

**Triggers**: Push to `main`, Pull Requests

The primary continuous integration workflow that runs on every code change.

**Jobs**:

- **lint**: Code quality checks
  - `go vet` - Static analysis
  - `gofmt -s` - Code formatting
  - `golangci-lint` - Comprehensive linting
  
- **test**: Unit tests
  - Runs on Go 1.23
  - Includes race detection (`-race`)
  - Generates coverage reports
  - Uploads coverage artifacts

- **test-integration**: Integration tests
  - Runs on all pull requests and pushes
  - Sets up SSH test environment on localhost
  - Runs integration test suite

- **build**: Verify build
  - Builds for `linux/amd64`
  - Uploads binary artifact

**Duration**: ~5 minutes

### 2. Release Workflow (`.github/workflows/release.yml`)

**Triggers**: Git tags matching `v*` (e.g., `v1.0.0`)

Automatically builds and publishes releases when a version tag is pushed.

**Jobs**:

- **goreleaser**: Automated release with GoReleaser
  - Builds for all target platforms
  - Creates compressed archives (tar.gz/zip)
  - Generates SHA256 checksums
  - Creates GitHub Release with notes

- **build-manual**: Fallback manual build
  - Matrix builds for multiple OS/arch combinations
  - Platforms: linux, darwin, windows
  - Architectures: amd64, arm64

**Output**: GitHub Release with binaries for all platforms

### 3. Code Quality Workflow (`.github/workflows/quality.yml`)

**Triggers**: Push to `main`, Pull Requests

Advanced code quality and security checks.

**Jobs**:

- **security**: Security scanning
  - `gosec` - Security vulnerability detection
  - Uploads security report

- **coverage**: Test coverage analysis
  - Generates HTML coverage report
  - Calculates coverage percentage
  - (Optional) Uploads to Codecov
  - Comments coverage on PRs

- **dependencies**: Dependency checks
  - Checks for outdated dependencies
  - Runs `govulncheck` for vulnerabilities
  - Verifies `go.mod` is tidy

### 4. Documentation Workflow (`.github/workflows/docs.yml`)

**Triggers**: Push to `main`, PRs affecting `docs/` or `*.md` files

Ensures documentation quality and validity.

**Jobs**:

- **markdown-lint**: Markdown style checking
- **link-check**: Validates all links in docs
- **spell-check**: Spell checking (informational)
- **validate-structure**: Checks required files exist

### 5. Integration Test Workflow (`.github/workflows/integration-test.yml`)

**Triggers**: Manual dispatch, Nightly schedule (2 AM UTC)

Comprehensive integration testing against real SSH environments.

**Jobs**:

- **integration-test**: Localhost testing
  - Sets up SSH server
  - Starts Docker test containers
  - Runs full integration suite

- **integration-test-remote**: Remote host testing
  - Only when manual trigger with custom host
  - Uses SSH key authentication
  - Tests against real remote systems

## Local Development

### Using the Makefile

The project includes a comprehensive Makefile for local development:

```bash
# Show all available targets
make help

# Build for current platform
make build

# Run unit tests
make test

# Run integration tests (requires SSH_TEST_HOST)
make test-integration

# Run linters
make lint

# Format code
make fmt

# Generate coverage report
make coverage

# Build for all platforms
make release

# Install to GOPATH/bin
make install

# Run all checks (lint + test)
make check

# Full CI pipeline locally
make ci

# Check development environment
make doctor
```

### Environment Variables

For integration tests:

```bash
# Required for integration tests
export SSH_TEST_HOST=your-ssh-host
export SSH_TEST_USER=your-username

# Optional
export SSH_TEST_PASSWORD=your-password  # Or use key-based auth
export SSH_TEST_PORT=22
```

### Pre-commit Hooks

The project supports pre-commit hooks to catch issues before committing:

**Installation**:

```bash
# Option 1: Using pre-commit framework
pip install pre-commit
pre-commit install

# Option 2: Manual git hook
ln -s ../../scripts/pre-commit.sh .git/hooks/pre-commit
```

**What the hooks check**:
- Code formatting (gofmt)
- Static analysis (go vet)
- Fast unit tests
- No hardcoded credentials
- No debug statements (warning only)

## Running Tests Locally

### Unit Tests

```bash
# All unit tests
make test

# Specific package
go test ./internal/ssh/...

# With coverage
make coverage

# Verbose output
make test VERBOSE=1
```

### Integration Tests

```bash
# Using Makefile
export SSH_TEST_HOST=localhost
make test-integration

# Direct with go test
go test -v -timeout=10m -tags=integration ./tests/integration/...

# Single test
go test -v -run TestEndToEnd -tags=integration ./tests/integration/
```

### Benchmarks

```bash
# Run benchmarks
go test -bench=. -benchmem ./...

# Specific benchmark
go test -bench=BenchmarkReconcile ./internal/reconcile/
```

## Release Process

### Creating a Release

1. **Update CHANGELOG.md**:
   ```markdown
   ## [1.0.0] - 2024-01-15
   
   ### Added
   - New feature
   
   ### Fixed
   - Bug fix
   ```

2. **Create and push tag**:
   ```bash
   git tag -a v1.0.0 -m "Release v1.0.0"
   git push origin v1.0.0
   ```

3. **Automated steps**:
   - GitHub Actions builds all platform binaries
   - Creates GitHub Release
   - Attaches binaries and checksums
   - Publishes release notes

### Version Numbering

Follow [Semantic Versioning](https://semver.org/):

- **MAJOR** (v2.0.0): Breaking changes
- **MINOR** (v1.1.0): New features, backward compatible
- **PATCH** (v1.0.1): Bug fixes, backward compatible

Pre-release versions:
- `v1.0.0-rc.1` - Release candidate
- `v1.0.0-beta.1` - Beta version
- `v1.0.0-alpha.1` - Alpha version

## Adding New Workflows

To add a new workflow:

1. Create `.github/workflows/your-workflow.yml`
2. Define trigger conditions
3. Add jobs and steps
4. Test with `workflow_dispatch` trigger
5. Update this documentation

**Example workflow skeleton**:

```yaml
name: Your Workflow

on:
  push:
    branches: [ main ]

jobs:
  your-job:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - run: make your-target
```

## Troubleshooting

### CI Failures

**Lint failures**:
```bash
# Fix formatting locally
make fmt

# Run linters
make lint
```

**Test failures**:
```bash
# Run specific failing test
go test -v -run TestName ./path/to/package

# With race detector
go test -race ./...
```

**Integration test failures**:
- Check SSH_TEST_HOST is accessible
- Verify Docker is running
- Check network connectivity
- Review workflow logs for specific errors

### Build Failures

**GoReleaser issues**:
```bash
# Test release locally
goreleaser release --snapshot --clean

# Validate configuration
goreleaser check
```

**Platform-specific build issues**:
```bash
# Build for specific platform
GOOS=linux GOARCH=amd64 go build ./cmd/rdhpf
```

## Performance Optimization

### Caching

All workflows use Go module caching:

```yaml
- uses: actions/setup-go@v5
  with:
    go-version: '1.23'
    cache: true  # Enables automatic caching
```

### Parallel Execution

- Tests run in parallel across Go versions
- Lint and test jobs run concurrently
- Build matrix parallelizes cross-platform builds

### Fast Feedback

- Fail fast: Stops on first failure
- Unit tests complete in <1 minute
- Full CI pipeline in ~5 minutes

## Badges

Add these badges to README.md:

```markdown
[![CI](https://github.com/your-org/rdhpf/actions/workflows/ci.yml/badge.svg)](https://github.com/your-org/rdhpf/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/your-org/rdhpf)](https://goreportcard.com/report/github.com/your-org/rdhpf)
[![codecov](https://codecov.io/gh/your-org/rdhpf/branch/main/graph/badge.svg)](https://codecov.io/gh/your-org/rdhpf)
[![License](https://img.shields.io/github/license/your-org/rdhpf)](LICENSE)
```

## Security

### Secrets Management

Required secrets (configure in repository settings):

- `CODECOV_TOKEN` - (Optional) For coverage uploads
- `SSH_TEST_PRIVATE_KEY` - (Optional) For remote integration tests
- `SSH_TEST_USER` - (Optional) For remote integration tests

Never commit:
- API keys
- Passwords
- SSH private keys
- Access tokens

The pre-commit hooks check for hardcoded credentials.

## Future Enhancements

Planned improvements:

- [ ] Docker image builds and publishing to registry
- [ ] Snap/deb/rpm package generation
- [ ] Automated dependency updates (Dependabot)
- [ ] Performance regression testing
- [ ] Canary deployments
- [ ] Multi-architecture Docker images

## Resources

- [GitHub Actions Documentation](https://docs.github.com/en/actions)
- [GoReleaser Documentation](https://goreleaser.com/)
- [Pre-commit Framework](https://pre-commit.com/)
- [Semantic Versioning](https://semver.org/)