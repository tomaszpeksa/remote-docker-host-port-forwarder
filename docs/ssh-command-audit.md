# SSH Command Audit - Shell Quoting Fix

## Date

2025-11-11

## Issue

Docker template syntax `{{...}}` was being expanded by the remote shell when passed as unquoted arguments to SSH commands, causing commands to fail.

## Root Cause

When executing `ssh host docker command --format {{json .}}`, the remote shell interprets `{{` and `}}` as glob patterns or brace expansion, mangling the arguments before Docker receives them.

## Solution

Wrap all docker commands containing template syntax in `sh -c 'docker command --format "{{...}}"'` to protect templates from shell expansion.

## Files Audited and Fixed

### 1. internal/docker/events.go (Lines 109-125)

**Command**: `docker events --format {{json .}}`

**Status**: ✅ FIXED

**Change**:

- Before: `args := [..., "docker", "events", "--format", "{{json .}}", ...]`
- After: `dockerCmd := "docker events --format '{{json .}}' ..."; args := [..., "sh", "-c", dockerCmd]`

### 2. internal/docker/inspect.go (Lines 49-59)

**Command**: `docker inspect <id> --format {{json .HostConfig.PortBindings}}`

**Status**: ✅ FIXED

**Change**:

- Before: `args := [..., "docker", "inspect", containerID, "--format", "{{json ...}}", ...]`
- After: `dockerCmd := "docker inspect ... --format '{{json ...}}'"; args := [..., "sh", "-c", dockerCmd]`

### 3. internal/manager/manager.go (Lines 533-541)

**Command**: `docker ps --format {{.ID}}`

**Status**: ✅ FIXED

**Change**:

- Before: `args := [..., "docker", "ps", "--format", "{{.ID}}"]`
- After: `dockerCmd := "docker ps --format '{{.ID}}'"; args := [..., "sh", "-c", dockerCmd]`

### 4. internal/ssh/master.go (Lines 147, 207, 266)

**Commands**: SSH control master setup and health checks

**Status**: ✅ NO ACTION NEEDED

**Reason**: No Docker template syntax used

### 5. internal/ssh/forward.go (Lines 195, 272)

**Commands**: SSH port forwarding (-L, -R flags)

**Status**: ✅ NO ACTION NEEDED

**Reason**: No Docker template syntax used

## Verification

### Build Status

```bash
go build ./cmd/rdhpf
# Exit code: 0 ✅
```

### Test Status

```bash
go test ./...
# All tests pass ✅
```

### Pattern Check

```bash
grep -rn '{{.*}}' internal/ | grep -v "sh -c" | grep -v "//"
# Results: All instances properly quoted within dockerCmd strings ✅
```

## Success Criteria Met


- [x] All SSH commands with `{{` templates now use `sh -c`
- [x] All templates are quoted: `'{{...}}'`
- [x] No unquoted `{{` patterns remain in args arrays
- [x] Build succeeds without errors
- [x] All existing tests pass

## Testing Recommendations


1. Integration test with real SSH host to verify commands execute correctly
2. Verify docker events stream starts successfully
3. Confirm docker inspect returns proper JSON
4. Check docker ps returns container IDs during startup reconciliation

## Related

- Fix for production bug in v0.1.3
- GitHub Issue: Shell quoting causes "docker: 'docker events' accepts no arguments"
- Will be released in v0.1.4