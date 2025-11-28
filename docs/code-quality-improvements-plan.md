# Code Quality Improvements Plan

## Executive Summary

Four targeted refactorings to reduce technical debt and improve maintainability. These address architectural issues that emerged during rapid stabilization. **Zero breaking changes** to public API or user-facing behavior.

**Total estimated effort:** 6-8 hours across all improvements
**Risk level:** Low (all changes are internal refactoring with comprehensive tests)
**Priority order:** Labels → SSH Args → Parsing → Logging

---

## Improvement 1: Centralize Docker Labels & Gate Fallback Behavior

### Current State

String literals scattered across 3 files:
- `internal/manager/manager.go:609` - `"rdhpf.test-infrastructure"`
- `internal/docker/inspect.go:120` - `"rdhpf.forward.*"`
- Tests use similar patterns

### Problems

1. **Typo risk** - string literals have no compile-time validation
2. **Hidden behavior** - label-based port discovery always active (test-only feature in production)
3. **Maintenance burden** - changing label schema requires grep-and-replace

### Solution

**New file:** `internal/docker/labels.go`
```go
package docker

const (
    // LabelTestInfrastructure marks containers to skip during reconciliation
    LabelTestInfrastructure = "rdhpf.test-infrastructure"
    
    // LabelForwardPrefix for custom port mappings (format: rdhpf.forward.LOCAL=REMOTE)
    LabelForwardPrefix = "rdhpf.forward."
)
```

**Configuration:** Add env flag `RDHPF_ENABLE_LABEL_PORTS=1` to opt-in

**Changes:**

1. `internal/docker/inspect.go` - use constants, check env before label fallback
2. `internal/manager/manager.go` - use constant for test-infrastructure check
3. `internal/config/config.go` - add `EnableLabelPorts bool` field (read from env)
4. Tests - set env flag explicitly
5. `docs/user-guide.md` - document label-based ports as opt-in advanced feature

### Impact

**Benefits:**

- ✅ Compile-time safety for label names
- ✅ Explicit opt-in for test-only behavior
- ✅ Single source of truth for label schema
- ✅ Self-documenting code (constants have godoc)

**Risks:**

- ⚠️ Tests need `RDHPF_ENABLE_LABEL_PORTS=1` - addressed by setting in test setup
- ⚠️ Breaking change for anyone using labels in production (unlikely, undocumented)

**Files changed:** 5 files

**Lines changed:** ~50 lines

**Tests needed:** 2 new tests (env flag behavior, constant usage)


---

## Improvement 2: Shared SSH Command Argument Builder

### Current State

SSH args constructed ad-hoc in 6 places:
- `internal/ssh/master.go` - Open, Close, Check (3 call sites)
- `internal/ssh/forward.go` - AddForward, CancelForward (2 call sites)
- `internal/docker/events.go` - Stream
- `internal/docker/inspect.go` - InspectPorts
- `internal/manager/manager.go` - reconcileStartup

Each repeats pattern:
```go
args := []string{"-S", controlPath}
if port != "" {
    args = append(args, "-p", port)
}
args = append(args, sshHost, ...)
```

### Problems

1. **Duplication** - same pattern copy-pasted 6+ times
2. **Inconsistency** - some use different orderings, some forget GlobalKnownHostsFile
3. **Maintenance** - adding SSH options requires touching 6 files
4. **Error-prone** - easy to forget port flag or use wrong order

### Solution


**New helper in:** `internal/ssh/command.go`
```go
package ssh

// CommandBuilder constructs SSH command arguments consistently
type CommandBuilder struct {
    host        string
    port        string
    controlPath string
    args        []string
}

// NewCommand creates a builder for SSH commands
func NewCommand(sshURL, controlPath string) *CommandBuilder {
    host, port := ParseHost(sshURL)
    return &CommandBuilder{
        host:        host,
        port:        port,
        controlPath: controlPath,
    }
}

// WithControlOp adds -O operation (check, exit, forward, cancel)
func (b *CommandBuilder) WithControlOp(op string) *CommandBuilder

// WithPortForward adds -L forward spec
func (b *CommandBuilder) WithPortForward(localPort, remotePort int) *CommandBuilder

// WithRemoteCommand adds command to execute on remote host
func (b *CommandBuilder) WithRemoteCommand(cmd string) *CommandBuilder

// Build returns final args array
func (b *CommandBuilder) Build() []string
```

**Refactor all call sites:**

```go
// Before
args := []string{"-S", controlPath}
if port != "" {
    args = append(args, "-p", port)
}
args = append(args, sshHost, "-O", "check")

// After
args := ssh.NewCommand(sshURL, controlPath).
    WithControlOp("check").
    Build()
```

### Impact

**Benefits:**

- ✅ DRY - single implementation of SSH arg logic
- ✅ Consistency - all SSH commands use same ordering
- ✅ Extensibility - easy to add common SSH options (e.g., ConnectTimeout)
- ✅ Testability - builder logic tested once, thoroughly
- ✅ Readability - fluent API documents intent

**Risks:**

- ⚠️ Abstraction complexity - mitigated by keeping builder simple and well-tested
- ⚠️ Performance - negligible (args array construction is not hot path)

**Files changed:** 7 files (1 new, 6 refactored)

**Lines changed:** ~120 lines (net reduction after dedup)

**Tests needed:** 5 new tests (builder variations), update 6 existing tests


---

## Improvement 3: Robust SSH URL Parsing & Validation

### Current State

`internal/ssh/util.go:ParseHost()` - simple string manipulation:
```go
func ParseHost(sshURL string) (host string, port string) {
    host = strings.TrimPrefix(sshURL, "ssh://")
    if idx := strings.LastIndex(host, ":"); idx != -1 {
        port = host[idx+1:]
        host = host[:idx]
    }
    return host, port
}
```

### Problems

1. **IPv6 fails** - `ssh://[::1]:2222` parsed incorrectly (returns `host="::", port="1]:2222"`)
2. **No validation** - accepts malformed URLs silently
3. **Ambiguous errors** - user can't tell if URL format is problem
4. **Undocumented** - no explicit IPv6 support policy

### Solution


**Enhanced parser:**
```go
// ParseHost extracts host and port from SSH URLs with full IPv6 support
// Formats supported:
//   ssh://user@host
//   ssh://user@host:port
//   ssh://user@[::1]:port  (IPv6 with brackets)
// Returns error for malformed URLs
func ParseHost(sshURL string) (host string, port string, error)
```

**Validation in config:**

```go
// internal/config/config.go
func (c *Config) Validate() error {
    if !strings.HasPrefix(c.Host, "ssh://") {
        return fmt.Errorf("SSH_HOST must start with ssh:// (got: %s)", c.Host)
    }
    
    host, port, err := ssh.ParseHost(c.Host)
    if err != nil {
        return fmt.Errorf("invalid SSH_HOST: %w", err)
    }
    
    // Disallow ambiguous IPv6 without brackets
    if strings.Count(host, ":") > 1 && !strings.HasPrefix(host, "[") {
        return fmt.Errorf("IPv6 addresses must use brackets: ssh://user@[::1]:port")
    }
    
    return nil
}
```

**Comprehensive tests in `tests/unit/ssh_parsehost_test.go`:**

- Valid formats (IPv4, IPv6 bracketed, hostnames)
- Invalid formats (no ssh://, unbracketed IPv6, malformed)
- Edge cases (IPv6 with zone ID, very long hostnames)

### Impact

**Benefits:**

- ✅ IPv6 support - enables use with IPv6-only infrastructure
- ✅ Early validation - fails fast with clear error messages
- ✅ Security - prevents injection via malformed URLs
- ✅ Documentation - explicit support policy
- ✅ User experience - helpful error messages guide fixes

**Risks:**

- ⚠️ Breaking change - malformed URLs that silently failed now error (good thing!)
- ⚠️ IPv6 testing - requires IPv6 test environment (use mocks in unit tests)

**Files changed:** 4 files

**Lines changed:** ~80 lines

**Tests needed:** 15 new test cases (various URL formats)

**Documentation:** Add IPv6 section to `docs/user-guide.md`


---

## Improvement 4: Normalize Structured Logging

### Current State

Inconsistent logging across codebase:
- `internal/docker/events.go:147` - logs `command` as string
- `internal/ssh/master.go:91` - logs `args` as formatted string
- Mixed key names: `host`, `sshHost`, `sshHostClean`
- Arrays logged as strings vs. structured

### Problems

1. **Parsing difficulty** - logs not machine-readable for analysis
2. **Inconsistency** - same concept has different key names
3. **Lost context** - command arrays printed as single string lose structure
4. **Grep-hostile** - hard to search for specific hosts/ports

### Solution


**Logging guideline in `docs/logging-guidelines.md`:**
```markdown
## Structured Logging Standards

### Key Names (Consistent Across Codebase)
- `sshHost` - SSH connection target (user@host format)
- `port` - Port number (integer)
- `controlPath` - SSH control socket path
- `containerID` - Docker container ID (12-char short form preferred)
- `localPort`, `remotePort` - Port forward endpoints

### Arrays and Commands
- Log command arrays as structured `[]string` not formatted strings
- Use `slog.Any("args", args)` not `fmt.Sprintf("%v", args)`

### Levels
- DEBUG: SSH operations, control path operations
- INFO: Container events, forward created/removed, startup/shutdown
- WARN: Recoverable errors, retries
- ERROR: Unrecoverable errors requiring user action
```

**Refactor logging calls:**

```go
// Before
logger.Info("executing command",
    "command", fmt.Sprintf("ssh -S %s %s", controlPath, sshHost))

// After  
logger.Info("executing SSH command",
    "sshHost", sshHost,
    "controlPath", controlPath,
    "args", args)  // logged as structured array
```

**Apply to:**

- `internal/docker/events.go` - normalize keys and log args
- `internal/docker/inspect.go` - same
- `internal/ssh/master.go` - same
- `internal/ssh/forward.go` - same
- `internal/manager/manager.go` - normalize keys

### Impact

**Benefits:**

- ✅ Machine-readable - logs easily parsed for debugging/monitoring
- ✅ Searchable - consistent keys enable reliable grepping
- ✅ Structured - command arrays preserved for analysis
- ✅ Debuggable - clear context for every operation
- ✅ Professional - follows industry best practices

**Risks:**

- ⚠️ Log format change - anyone parsing logs needs update (unlikely)
- ⚠️ Volume - structured logs slightly more verbose (acceptable trade-off)

**Files changed:** 6 files (5 code, 1 doc)

**Lines changed:** ~60 lines

**Tests needed:** No new tests (logging doesn't affect behavior)

**Documentation:** New `docs/logging-guidelines.md`


---

## Implementation Strategy

### Recommended Order

#### Phase 1: Labels (2 hours)

- Low risk, high value
- Establishes pattern for constants
- Can be done independently

#### Phase 2: SSH Args Builder (3 hours)

- Medium complexity
- Benefits from Phase 1 patterns
- Reduces surface area for Phase 3

#### Phase 3: SSH Parsing (2 hours)

- Builds on Phase 2 refactoring
- Validation catches integration issues early
- Can be done before Phase 4

#### Phase 4: Logging (1 hour)

- Lowest risk (no behavior change)
- Can be done anytime
- Quick win for debuggability

### Testing Approach

1. Run **all existing tests** after each phase (ensure no regressions)
2. Add **new unit tests** for new functionality
3. Run **integration tests locally** (`make itest`) after Phase 2 & 3
4. No new integration tests needed (internal refactoring only)

### Rollout Safety

- All changes are internal implementation details
- No API changes, no user-facing behavior changes
- Can be rolled back easily (each phase is atomic commit)
- Documentation updates accompany code changes

---

## Success Metrics

### Code Quality
- [ ] Zero string literals for labels (`git grep "rdhpf\."` returns only constants/docs)
- [ ] Single SSH arg construction pattern (6 files refactored)
- [ ] All SSH URLs validated on startup (errors not silently ignored)
- [ ] Consistent log key names across codebase

### Correctness

- [ ] All existing tests pass (100% green)
- [ ] New tests cover edge cases (IPv6, invalid URLs, env flags)
- [ ] Integration tests pass locally and in CI

### Maintainability

- [ ] New constants file is single source of truth
- [ ] SSH command builder documented and tested
- [ ] Logging guidelines prevent future inconsistency
- [ ] IPv6 support documented and tested

---

## Risks & Mitigation

### Breaking Changes (Low Risk)

**Risk:** Label-based ports gated behind env flag breaks existing usage

**Likelihood:** Very low (undocumented, test-only feature)

**Mitigation:** Document migration path, add deprecation warning in logs if labels detected without flag

**Risk:** Strict SSH URL validation rejects currently-working malformed URLs

**Likelihood:** Low (most users follow docs, which show correct format)

**Mitigation:** Clear error messages guide users to fix URLs

### Test Coverage (Medium Risk)

**Risk:** Refactoring introduces subtle bugs not caught by existing tests

**Mitigation:**

- Add new unit tests for all new functionality
- Run integration tests after Phases 2 & 3
- Deploy to staging before production (if available)

### IPv6 Testing (Medium Risk)

**Risk:** IPv6 parsing works in unit tests but fails with real infrastructure

**Mitigation:**

- Document IPv6 support as "best effort" initially
- Mock SSH calls in unit tests
- Add integration test for IPv6 once test infrastructure supports it

---

## Alternative Approaches Considered

### Labels: Runtime Detection vs. Env Flag

**Rejected:** Auto-detect labels and enable fallback dynamically

**Reason:** Implicit behavior is error-prone; explicit opt-in is clearer

### SSH Args: Struct vs. Builder vs. Function

**Rejected:** Simple struct with Build() method

**Reason:** Builder pattern is more extensible and readable

### Parsing: Drop IPv6 Support

**Rejected:** Document IPv6 as unsupported

**Reason:** Low effort to support, high user value; future-proofs codebase

### Logging: Structured Logger Wrapper

**Rejected:** Wrap slog with custom wrapper enforcing keys

**Reason:** Over-engineering; guidelines + code review sufficient


---

## Conclusion

Four focused refactorings that address real pain points without breaking changes. Each improvement is independently valuable but together they significantly reduce technical debt and improve long-term maintainability. Estimated 6-8 hours total effort with comprehensive testing ensures zero regression risk.

**Recommendation:** Proceed with implementation in the recommended order (Labels → SSH Args → Parsing → Logging), committing each phase separately for atomic rollback capability.