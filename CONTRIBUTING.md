# Contributing to Remote Docker Host Port Forwarder

Thank you for your interest in contributing! This document provides guidelines and instructions for contributing to the project.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Development Workflow](#development-workflow)
- [Running Tests](#running-tests)
- [Code Quality Standards](#code-quality-standards)
- [Submitting Pull Requests](#submitting-pull-requests)
- [Release Process](#release-process)

## Code of Conduct

This project follows the standard open source code of conduct. Be respectful, inclusive, and constructive in all interactions.

## Getting Started

### Prerequisites

- **Go 1.27.2 or newer** for source consumers; repository builds pin **1.27.2**.
- **Linux or macOS 13+**, amd64 or arm64, for the managed development tools.
  Newly built macOS binaries also require macOS 13; the module directive is not
  an independent promise of older binary OS compatibility.
- Git, Make, and OpenSSH. Docker is needed only for the isolated integration harness.
- **mise 2026.10.8**, obtained from its [official release](https://github.com/jdx/mise/releases/tag/v2026.10.8).
  Verify the release's signed checksums before executing the bootstrap binary.
  Add it to the current shell's PATH; no global mise settings or profile edits are required.

### Initial Setup

After cloning this repository, inspect `mise.toml`, then explicitly trust it:

```bash
mise --version # 2026.10.8
mise trust
MISE_LOCKED_SCOPES=project mise install --locked core:go aqua:golangci/golangci-lint aqua:jqlang/jq
mise exec -- make deps
mise exec -- make check
mise exec -- make build
```

The following Make/Go examples assume `mise exec --` or an already activated
mise shell. Make checks the required managed executable paths before running
commands. Missing tools or ambient copies fail; a missing linter is not success.
The module graph remains owned by `go.mod`/`go.sum`, not mise.

### Installing Development Tools

`mise.toml` owns exact tool pins; `mise.lock` covers Linux/macOS x64/arm64 and
`.mise/locks/` holds the npm dependency graph. The root's lock policy applies to
this project. The **command-local** `MISE_LOCKED_SCOPES=project` assignment plus
named `--locked` installation avoids requiring unrelated global/system locks
and prevents lockfile rewrites. Do not replace these recipes with an argumentless
`mise install --locked`, or set global locked scopes.

Ordinary `mise exec` and activation do not install absent optional tools:
`auto_install=false`, no lazy declarations. Keep that setting in effect. Explicit
user environment/local-config overrides are outside the reproducible recipe.
Lockfiles verify downloads, not arbitrary later modifications to an installed cache;
Go-backed tools lock their top-level version, with native module sums still required.

Optional sets (install only what you use):

```bash
# Documentation: Node is tooling-only, not an application runtime dependency.
MISE_LOCKED_SCOPES=project mise install --locked core:node npm:markdownlint-cli2
mise exec -- scripts/check-tools.sh node markdownlint-cli2
mise exec -- markdownlint-cli2 'docs/**/*.md' README.md CONTRIBUTING.md

# Hooks: install the ordinary and documentation sets first.
MISE_LOCKED_SCOPES=project mise install --locked core:python aqua:pre-commit/pre-commit
mise exec -- scripts/check-tools.sh python pre-commit
mise exec -- pre-commit run --all-files
# Optional, explicit hook registration in your clone:
mise exec -- pre-commit install

# Security/dependency reports: the base release linter supplies gosec.
MISE_LOCKED_SCOPES=project mise install --locked go:golang.org/x/vuln/cmd/govulncheck go:github.com/psampaz/go-mod-outdated
mise exec -- make security
mise exec -- scripts/check-tools.sh govulncheck go-mod-outdated
mise exec -- govulncheck ./...

# Nonpublishing release validation:
MISE_LOCKED_SCOPES=project mise install --locked aqua:goreleaser/goreleaser
mise exec -- scripts/check-tools.sh goreleaser
mise exec -- goreleaser check

# Evidence download helper only; not needed to build:
MISE_LOCKED_SCOPES=project mise install --locked aqua:cli/cli
mise exec -- scripts/check-tools.sh gh jq
```

To opt into **all project tools**, explicitly name their union:

```bash
MISE_LOCKED_SCOPES=project mise install --locked core:go aqua:golangci/golangci-lint aqua:jqlang/jq go:golang.org/x/vuln/cmd/govulncheck go:github.com/psampaz/go-mod-outdated aqua:goreleaser/goreleaser aqua:cli/cli core:node npm:markdownlint-cli2 core:python aqua:pre-commit/pre-commit
```

golangci-lint, gh and GoReleaser use pinned official release distributions through
mise's aqua backend. Keep checksum/provenance verification enabled. Their embedded
Go compiler is determined by upstream, not this project's Go pin. Ordinary Go-backed
tools such as govulncheck retain their documented installation route. A current
version or successful installation is not security clearance: known tool reports
remain unresolved, with no compiler/graph workaround or risk waiver.

Security uses the release linter's unchanged gosec 2.29.0 engine and x/tools 0.50.0
importer. Standalone gosec 2.29.0 cannot load Go 1.27.2 export data; rebuilding its
unchanged module does not fix it.
`.golangci-security.yml` enables all gosec rules and SSA analyzers independently
of ordinary lint exclusions, at medium severity/confidence. `make security`
retains its `./...` scope; hosted quality scanning retains `./cmd/... ./internal/...`.
Reports are **native golangci JSON**, accompanied by stdout, stderr, scanner exit
and actual package scope. Missing/malformed reports, loading errors and findings
fail the gate; zero findings from an incomplete scan are not success.

Ordinary lint preserves the previous default checks: staticcheck/gosimple plus
v1's standard-error-handling exclusions. It does not silently add a style sweep
or change production cleanup error policy. The first pre-commit hook verifies
all required providers and stops the hook run on failure.

## Development Workflow

### 1. Create a Branch

Always create a feature branch for your work:

```bash
# Feature branch
git checkout -b feature/your-feature-name

# Bug fix branch
git checkout -b fix/bug-description

# Documentation branch
git checkout -b docs/documentation-update
```

### 2. Make Changes

- Write clean, idiomatic Go code
- Follow the existing code style
- Add tests for new functionality
- Update documentation as needed
- Keep commits focused and atomic

### 3. Test Your Changes

Before committing:

```bash
# Format code
make fmt

# Run linters
make lint

# Run tests
make test

# Run all checks
make check
```

### 4. Commit Your Changes

Write clear, descriptive commit messages:

```bash
git add .
git commit -m "feat: add new feature for X

- Implement feature Y
- Add tests for Z
- Update documentation

Closes #123"
```

Commit message format:
- `feat:` - New feature
- `fix:` - Bug fix
- `docs:` - Documentation changes
- `test:` - Test additions/changes
- `refactor:` - Code refactoring
- `perf:` - Performance improvements
- `ci:` - CI/CD changes

### 5. Push and Create PR

```bash
git push origin feature/your-feature-name
```

Then create a Pull Request on GitHub.

## Running Tests

### Unit Tests

```bash
# All unit tests
make test

# Specific package
go test ./internal/ssh/...

# With verbose output
make test VERBOSE=1

# With coverage
make coverage
```

### Integration Tests

Integration tests use a containerized SSH server with Docker socket access. Run this harness only on an isolated Docker daemon: tests create and stop containers.

```bash
# Start test environment (SSH container with Docker socket mount)
make itest-up

# Run integration tests
make itest

# Stop test environment
make itest-down
```

For an already configured isolated host, set `SSH_TEST_HOST=ssh://user@host:port`
and run `make test-integration`. SSH authentication uses your SSH configuration
or the optional `SSH_TEST_KEY_PATH` for fixture commands.

The two disruptive S06 recovery scenarios are hosted-only and produce named
skips in these local entry points, before changing SSH configuration, acquiring
publisher ownership or creating backends. Only the disposable Actions job selects
`RDHPF_HOSTED_RECOVERY=1`; selection retains strict runner/target/key checks and
requires both scenarios to pass. Do not set this variable for a local host.
This exclusion does not change the requested-infrastructure preflight below.

A nonempty `SSH_TEST_HOST` explicitly requests integration: unavailable SSH/Docker
fails preflight within 10 seconds, rather than reporting success without tests.
Selecting the package (or a build tag) alone does not request infrastructure;
without the host, remote scenarios self-skip. `make test` and `make coverage`
clear these integration variables and never require the harness.

For example, after configuring the host:

```bash
go test -v -count=1 -timeout=3m -run '^TestManager_ContainerWithOnePort$' ./tests/integration/
```

The one-port fixture waits up to five seconds for its owned nginx backend before
checking the original forwarded TCP/HTTP assertions. This is not a forwarded-request
retry. See [CI test evidence](docs/ci-cd.md) for result and coverage interpretation.

### Running All Tests

```bash
# Local build, lint and infrastructure-independent tests (not the Docker harness)
make ci
```

## Code Quality Standards

### Code Style

- Follow [Effective Go](https://golang.org/doc/effective_go.html)
- Use `gofmt -s` for formatting
- Keep functions small and focused
- Use meaningful variable names
- Add comments for exported functions

**Example**:

```go
// Forward represents an SSH port forward configuration.
// It maps a local port to a remote destination through an SSH tunnel.
type Forward struct {
    LocalPort  int    // Local port to listen on
    RemoteHost string // Destination host
    RemotePort int    // Destination port
}

// NewForward creates a new Forward with validation.
func NewForward(local int, remote string, port int) (*Forward, error) {
    if local < 1 || local > 65535 {
        return nil, fmt.Errorf("invalid local port: %d", local)
    }
    // ... validation
    return &Forward{
        LocalPort:  local,
        RemoteHost: remote,
        RemotePort: port,
    }, nil
}
```

### Testing Standards

- Write tests for all new code
- Aim for >80% code coverage
- Use table-driven tests when appropriate
- Test edge cases and error conditions
- Use descriptive test names

**Example**:

```go
func TestForwardValidation(t *testing.T) {
    tests := []struct {
        name        string
        local       int
        remote      string
        port        int
        expectError bool
    }{
        {
            name:        "valid forward",
            local:       8080,
            remote:      "localhost",
            port:        80,
            expectError: false,
        },
        {
            name:        "invalid local port",
            local:       -1,
            remote:      "localhost",
            port:        80,
            expectError: true,
        },
        // ... more cases
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            _, err := NewForward(tt.local, tt.remote, tt.port)
            if (err != nil) != tt.expectError {
                t.Errorf("expected error: %v, got: %v", tt.expectError, err)
            }
        })
    }
}
```

### Documentation Standards

- Document all exported types and functions
- Include code examples in documentation
- Keep README.md up to date
- Update CHANGELOG.md for user-facing changes
- Add inline comments for complex logic

### Error Handling

- Return errors, don't panic (except for truly exceptional cases)
- Wrap errors with context using `fmt.Errorf("context: %w", err)`
- Use custom error types for specific error conditions
- Log errors at appropriate levels

**Example**:

```go
func (m *Manager) Start() error {
    if err := m.connectSSH(); err != nil {
        return fmt.Errorf("failed to connect SSH: %w", err)
    }
    
    if err := m.setupForwards(); err != nil {
        return fmt.Errorf("failed to setup forwards: %w", err)
    }
    
    return nil
}
```

### Performance Considerations

- Avoid unnecessary allocations
- Use buffered channels appropriately
- Close resources with `defer`
- Profile before optimizing
- Document performance-critical code

## Submitting Pull Requests

### Before Submitting

Checklist:
- [ ] Code is formatted (`make fmt`)
- [ ] All tests pass (`make test`)
- [ ] Linters pass (`make lint`)
- [ ] New tests added for new features
- [ ] Documentation updated
- [ ] CHANGELOG.md updated (if user-facing change)
- [ ] Commits are clean and descriptive

### PR Description Template

```markdown
## Description

Brief description of the changes.

## Type of Change

- [ ] Bug fix (non-breaking change which fixes an issue)
- [ ] New feature (non-breaking change which adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Documentation update

## Testing

Describe how you tested your changes:
- Unit tests added/updated
- Integration tests added/updated
- Manual testing performed

## Checklist

- [ ] Code follows project style guidelines
- [ ] Self-review completed
- [ ] Comments added for complex code
- [ ] Documentation updated
- [ ] Tests added/updated
- [ ] All tests pass
- [ ] No new warnings

## Related Issues

Closes #(issue number)
```

### Review Process

1. **Automated Checks**: CI/CD runs automatically
   - Linting
   - Unit tests
   - Integration tests (on label)
   - Code coverage

2. **Code Review**: Maintainer reviews code
   - Code quality
   - Test coverage
   - Documentation
   - Design decisions

3. **Revisions**: Address feedback
   - Make requested changes
   - Push updates to same branch
   - CI runs again automatically

4. **Approval**: Once approved
   - Squash and merge (preferred)
   - Or regular merge for feature branches

### PR Labels

- `bug` - Bug fixes
- `enhancement` - New features
- `documentation` - Documentation updates
- `good first issue` - Good for newcomers
- `help wanted` - Looking for contributors
- `run-integration-tests` - Trigger integration tests in CI

## Release Process

Releases are automated via GitHub Actions when a tag is pushed.

### For Maintainers

1. **Update CHANGELOG.md**:
   ```markdown
   ## [1.0.0] - 2024-01-15
   
   ### Added
   - Feature X
   
   ### Changed
   - Improvement Y
   
   ### Fixed
   - Bug Z
   ```

2. **Create and push tag**:
   ```bash
   git tag -a v1.0.0 -m "Release v1.0.0"
   git push origin v1.0.0
   ```

3. **Automated process**:
   - GitHub Actions builds binaries
   - Creates GitHub Release
   - Uploads artifacts

### Version Numbering

Follow [Semantic Versioning](https://semver.org/):
- **1.0.0** → **2.0.0**: Breaking changes
- **1.0.0** → **1.1.0**: New features
- **1.0.0** → **1.0.1**: Bug fixes

## Getting Help

- **Issues**: For bugs and feature requests
- **Discussions**: For questions and ideas
- **Documentation**: See `docs/` directory
- **CI/CD**: See `docs/ci-cd.md`

## Additional Resources

- [Go Documentation](https://golang.org/doc/)
- [Effective Go](https://golang.org/doc/effective_go.html)
- [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- [GitHub Actions](https://docs.github.com/en/actions)

## License

By contributing, you agree that your contributions will be licensed under the same license as the project (see LICENSE file).

Thank you for contributing! 🎉