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

Required tools:
- **Go 1.23+** - Programming language
- **Git** - Version control
- **Make** - Build automation
- **Docker** - For integration testing

Optional but recommended:
- **golangci-lint** - Comprehensive Go linter
- **gosec** - Security scanner
- **pre-commit** - Git hooks framework

### Initial Setup

1. **Fork and clone the repository**:
   ```bash
   git clone https://github.com/your-username/rdhpf.git
   cd rdhpf
   ```

2. **Install dependencies**:
   ```bash
   make deps
   ```

3. **Verify your environment**:
   ```bash
   make doctor
   ```

4. **Build the project**:
   ```bash
   make build
   ```

5. **Run tests**:
   ```bash
   make test
   ```

### Installing Development Tools

**golangci-lint**:
```bash
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | \
  sh -s -- -b $(go env GOPATH)/bin v1.55.2
```

**gosec**:
```bash
go install github.com/securego/gosec/v2/cmd/gosec@latest
```

**pre-commit**:
```bash
pip install pre-commit
pre-commit install
```

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

Integration tests require an SSH server and Docker:

```bash
# Set up test environment
export SSH_TEST_HOST=localhost
export SSH_TEST_USER=testuser
export SSH_TEST_PASSWORD=testpass

# Run integration tests
make test-integration
```

Or run specific integration tests:

```bash
go test -v -timeout=10m -tags=integration ./tests/integration/end_to_end_test.go
```

### Running All Tests

```bash
# Complete test suite
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