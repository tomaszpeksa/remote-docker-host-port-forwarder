# DOCKER_HOST Simplification Plan

## Overview

Simplify rdhpf configuration by **removing the `--host` flag** and using **only the `DOCKER_HOST` environment variable**, aligning with standard Docker CLI conventions.

## Current State Analysis

### How Host is Currently Used

1. **CLI Flags** ([`cmd/rdhpf/main.go`](../cmd/rdhpf/main.go)):
   - `--host` flag required on both `run` and `status` commands
   - Format: `ssh://user@host`
   - Stored in `flagHost` global variable

2. **Config Struct** ([`internal/config/config.go`](../internal/config/config.go)):
   - `Host string` field validated to require `ssh://` prefix
   - Passed to all components (SSH master, event reader, reconciler, etc.)

3. **File Path Generation**:
   - State file: `~/.rdhpf/{hash}.state.json`
   - Socket file: `~/.rdhpf/{hash}.sock`
   - Hash derived from host string for uniqueness

4. **Throughout Codebase**:
   - SSH operations: connection, port forwarding, Docker commands
   - Documentation: user guide, README, troubleshooting
   - Tests: integration and unit tests pass host

## Benefits of DOCKER_HOST-Only Approach

✅ **Consistency**: Standard Docker environment variable  
✅ **Simpler UX**: No flags needed (`rdhpf run` instead of `rdhpf run --host ssh://...`)  
✅ **Fewer Parameters**: Less code, fewer validation points  
✅ **Standard Practice**: Aligns with Docker CLI (`docker`, `docker-compose`)  
✅ **CI-Friendly**: Already set in CI environments using remote Docker  

## Implementation Plan

### Phase 1: Core Changes

#### 1.1 Update CLI (`cmd/rdhpf/main.go`)

**Remove:**
```go
var flagHost string

runCmd.Flags().StringVar(&flagHost, "host", "", "SSH host...")
statusCmd.Flags().StringVar(&flagHost, "host", "", "SSH host...")
runCmd.MarkFlagRequired("host")
statusCmd.MarkFlagRequired("host")
```

**Add:**
```go
func getDockerHost() (string, error) {
    host := os.Getenv("DOCKER_HOST")
    if host == "" {
        return "", fmt.Errorf("DOCKER_HOST environment variable is required")
    }
    return host, nil
}
```

**Update `runMain()`:**
```go
func runMain(cmd *cobra.Command, args []string) error {
    host, err := getDockerHost()
    if err != nil {
        return err
    }
    
    cfg := &config.Config{
        Host:     host,
        LogLevel: logLevel,
    }
    // ... rest unchanged
}
```

**Update `runStatus()`:**
```go
func runStatus(cmd *cobra.Command, args []string) error {
    host, err := getDockerHost()
    if err != nil {
        return err
    }
    
    forwards, err := getActiveForwards(ctx, host)
    // ... rest unchanged
}
```

#### 1.2 Update Config (`internal/config/config.go`)

**Update validation message:**
```go
func (c *Config) Validate() error {
    if c.Host == "" {
        return fmt.Errorf("DOCKER_HOST environment variable is required")
    }
    if !strings.HasPrefix(c.Host, "ssh://") {
        return fmt.Errorf("DOCKER_HOST must be in ssh://user@host format, got: %s", c.Host)
    }
    // ... rest unchanged
}
```

### Phase 2: Documentation Updates

#### 2.1 README.md

**Before:**
```bash
rdhpf run --host ssh://user@remote-docker-host
rdhpf status --host ssh://user@remote-docker-host --format json
```

**After:**
```bash
export DOCKER_HOST=ssh://user@remote-docker-host
rdhpf run
rdhpf status --format json
```

#### 2.2 docs/user-guide.md

**Update Configuration Section:**
```markdown
## Configuration

rdhpf uses the standard `DOCKER_HOST` environment variable:

```bash
export DOCKER_HOST=ssh://user@remote-host
rdhpf run
```

**Environment Variables:**
- `DOCKER_HOST`: SSH connection string in `ssh://user@host` format (required)
- `RDHPF_LOG_LEVEL`: One of `trace`, `debug`, `info`, `warn`, `error`
- `RDHPF_ENABLE_LABEL_PORTS`: Set to `1` for label-based port discovery (testing)

**CLI Flags (`rdhpf run`):**
- `--log-level`: Override log level
- `--trace`: Maximum verbosity

**CLI Flags (`rdhpf status`):**
- `--format`: Output format (`table`, `json`, `yaml`)
```

#### 2.3 docs/troubleshooting.md

Update all examples to use `DOCKER_HOST` instead of `--host` flag.

### Phase 3: Test Updates

#### 3.1 Integration Tests

**Before:**
```bash
./rdhpf run --host ssh://testuser@localhost:2222
```

**After:**
```bash
export DOCKER_HOST=ssh://testuser@localhost:2222
./rdhpf run
```

**Update test helpers:**
```go
func getTestDockerHost() string {
    host := os.Getenv("DOCKER_HOST")
    if host == "" {
        host = os.Getenv("SSH_TEST_HOST") // Fallback for backward compat
    }
    return host
}
```

#### 3.2 Unit Tests

Update all tests that create configs to use `DOCKER_HOST`:

```go
func TestConfig(t *testing.T) {
    os.Setenv("DOCKER_HOST", "ssh://user@host")
    defer os.Unsetenv("DOCKER_HOST")
    // ... test
}
```

### Phase 4: User Migration

#### 4.1 Migration Guide

Create `docs/migration-v2.md`:

```markdown
# Migration Guide: v1.x to v2.0

## DOCKER_HOST Environment Variable

v2.0 simplifies configuration by using only the `DOCKER_HOST` environment variable.

### Before (v1.x)
```bash
rdhpf run --host ssh://user@remote-host
rdhpf status --host ssh://user@remote-host
```

### After (v2.0)
```bash
export DOCKER_HOST=ssh://user@remote-host
rdhpf run
rdhpf status
```

### Shell Aliases (Optional)

For quick switching between hosts:

```bash
# ~/.bashrc or ~/.zshrc
alias rdhpf-dev='DOCKER_HOST=ssh://user@dev-host rdhpf'
alias rdhpf-prod='DOCKER_HOST=ssh://user@prod-host rdhpf'
```

### CI/CD

No changes needed if `DOCKER_HOST` was already set. If using `--host` flag:

**Before:**
```yaml
- name: Forward ports
  run: rdhpf run --host ssh://docker@build-server &
```

**After:**
```yaml
- name: Forward ports
  env:
    DOCKER_HOST: ssh://docker@build-server
  run: rdhpf run &
```
```

## Implementation Order

1. ✅ **Phase 1**: Core code changes (`cmd/`, `internal/config/`)
2. ✅ **Phase 2**: Documentation updates (README, user guide, troubleshooting)
3. ✅ **Phase 3**: Test updates (integration, unit, CI)
4. ✅ **Phase 4**: Migration guide and release notes

## Breaking Changes

⚠️ **This is a breaking change** requiring a major version bump (v2.0.0):
- Removes `--host` flag from all commands
- Requires `DOCKER_HOST` environment variable
- Users must update scripts and CI pipelines

## Rollout Strategy

1. **Branch**: Create `feature/docker-host-only` branch
2. **Implement**: All changes from phases 1-3
3. **Test**: Run full test suite
4. **Documentation**: Complete phase 4
5. **Release**: Tag as v2.0.0-rc1 for early feedback
6. **Announce**: Update README with migration guide link
7. **Stable**: Release v2.0.0 after validation period

## Testing Checklist

- [ ] Unit tests pass with DOCKER_HOST
- [ ] Integration tests pass with DOCKER_HOST
- [ ] CI tests pass with DOCKER_HOST
- [ ] Manual testing: `rdhpf run` works
- [ ] Manual testing: `rdhpf status` works
- [ ] Error message when DOCKER_HOST not set is clear
- [ ] State/socket file paths work correctly
- [ ] Multiple instances with different DOCKER_HOST values work

## Files to Modify

### Code
- [ ] `cmd/rdhpf/main.go` - Remove flags, read DOCKER_HOST
- [ ] `internal/config/config.go` - Update validation messages
- [ ] All test files using host configuration

### Documentation
- [ ] `README.md` - Update all examples
- [ ] `docs/user-guide.md` - Update configuration section
- [ ] `docs/troubleshooting.md` - Update examples
- [ ] `docs/migration-v2.md` - Create migration guide
- [ ] `docs/ci-integration-tests.md` - Update test examples

### Tests
- [ ] `tests/integration/*.go` - Use DOCKER_HOST
- [ ] `tests/unit/*.go` - Use DOCKER_HOST where applicable
- [ ] `.github/workflows/*.yml` - Update CI configuration

## Open Questions

1. **Backward Compatibility**: Should we support `--host` with deprecation warning?
   - **Decision**: No, clean break for v2.0.0 (simpler codebase)

2. **SSH_TEST_HOST vs DOCKER_HOST**: Should tests use DOCKER_HOST only?
   - **Decision**: Use DOCKER_HOST primarily, SSH_TEST_HOST as fallback for existing setups

3. **Default value**: Should we default to `ssh://` if DOCKER_HOST doesn't have scheme?
   - **Decision**: No, require explicit `ssh://` for clarity

## Success Criteria

✅ No `--host` flag in codebase  
✅ All commands read from `DOCKER_HOST` only  
✅ All tests pass  
✅ Documentation complete and accurate  
✅ Migration guide clear and helpful  
✅ Error messages guide users to set DOCKER_HOST  

---

**Status**: Planning Complete  
**Target Version**: v2.0.0  
**Estimated Effort**: 4-6 hours  
**Risk**: Low (clear scope, good test coverage)