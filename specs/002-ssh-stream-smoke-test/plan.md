# Plan: Enable Integration Tests in CI

**Goal:** Run existing integration tests in GitHub Actions on every PR to catch bugs like v0.1.4 before production.

---

## Current State Analysis

### Existing Integration Tests (6 tests)

**Location:** `tests/integration/`

1. **SSH Master Tests** (`ssh_master_test.go` - 8 test functions)
   - `TestSSHMaster_OpenAndClose`
   - `TestSSHMaster_Check`
   - `TestSSHMaster_CheckAfterClose`
   - `TestSSHMaster_MultipleOpenClose`
   - `TestSSHMaster_ControlPathReleased`
   - `TestSSHMaster_ConcurrentCheck`
   - `TestSSHMaster_ContextCancellation`
   - `TestSSHMaster_InvalidHost`

2. **End-to-End Tests** (`end_to_end_test.go` - 3 test functions)
   - `TestManager_ContainerWithNoPorts`
   - `TestManager_ContainerWithOnePort` ⭐ *Would catch v0.1.4 bug*
   - `TestManager_ContainerWithThreePorts`

3. **Other Integration Tests**
   - `conflict_test.go`
   - `connection_recovery_test.go`
   - `graceful_shutdown_test.go`
   - `status_test.go`

### Current CI Status

**File:** `.github/workflows/integration-test.yml`

**Triggers:**
- ❌ **NOT on pull_request** (tests don't run on PRs)
- ✅ Manual `workflow_dispatch`
- ✅ Scheduled (nightly at 2 AM UTC)

**Problem:** Integration tests are completely skipped during development/PR review.

**Why tests are skipped:**
```go
func getTestSSHHost(t *testing.T) string {
    host := os.Getenv("SSH_TEST_HOST")
    if host == "" {
        t.Skip("Skipping integration test: SSH_TEST_HOST not set")
    }
    return host
}
```

All integration tests call `getTestSSHHost(t)` → if `SSH_TEST_HOST` is not set → tests skip.

---

## Root Cause: Why CI Doesn't Run Integration Tests

### GitHub Actions Workflow Analysis

The workflow `.github/workflows/integration-test.yml` has:

1. **SSH setup code** (lines 33-67) ✅ Already implemented
   - Installs openssh-server
   - Creates test user
   - Configures passwordless auth
   - **BUT:** This runs on localhost

2. **Docker setup** (lines 69-79) ✅ Already implemented
   - Starts test containers
   - nginx on port 8080
   - redis on port 6379

3. **Environment variables** (lines 82-87) ⚠️ Partially set
   ```yaml
   SSH_TEST_HOST: ${{ github.event.inputs.ssh_host || 'localhost' }}
   ```
   - Sets `SSH_TEST_HOST` but only for manual workflow
   - **Missing `ssh://` prefix!**

4. **PR trigger** (line 3) ❌ Missing
   ```yaml
   on:
     workflow_dispatch:  # ✅ Has this
     schedule:           # ✅ Has this
     # pull_request:    # ❌ MISSING THIS!
   ```

---

## The Fix: Enable Tests on PRs

### Strategy

**Simple approach:**
1. Add `pull_request` trigger to workflow
2. Fix `SSH_TEST_HOST` to include `ssh://` prefix
3. Make sure user has Docker access
4. Run integration tests

**No new code needed!** Just workflow configuration changes.

### Changes Required

#### Change 1: Add PR Trigger

```yaml
on:
  pull_request:           # ADD THIS
    branches: [ main ]
  workflow_dispatch:
  schedule:
    - cron: '0 2 * * *'
```

#### Change 2: Fix SSH_TEST_HOST Format

Current (broken):
```yaml
SSH_TEST_HOST: ${{ github.event.inputs.ssh_host || 'localhost' }}
```

Fixed:
```yaml
SSH_TEST_HOST: ssh://testuser@localhost
```

The tests expect `ssh://user@host` format, but workflow only provides `localhost`.

#### Change 3: Ensure Test User Has Docker Access

The workflow already has this (line 58):
```bash
sudo usermod -aG docker testuser
```

But we need to verify the test can actually use Docker over SSH.

---

## Implementation Plan

### Phase 1: Minimal Changes to Enable CI

**File:** `.github/workflows/integration-test.yml`

**Changes:**
1. Line 3: Add `pull_request` trigger
2. Line 83: Fix `SSH_TEST_HOST` format
3. Line 93: Run tests (already there, just verify)

**Expected outcome:** Tests run on every PR

### Phase 2: Verify Tests Pass

**Challenges we might face:**

1. **Docker socket permissions**
   - Test user might not have immediate access to Docker socket
   - May need `newgrp docker` or session restart

2. **SSH known_hosts**
   - Already handled (lines 60-62)

3. **Test timing**
   - E2E tests might be slow (~30-60s each)
   - Consider running in parallel or subset first

4. **Resource limits**
   - GitHub runners have limited resources
   - Multiple Docker containers might strain CPU/memory

### Phase 3: Incremental Rollout

**Option A: Run all tests immediately**
- Pros: Maximum coverage
- Cons: Might be slow, might have failures

**Option B: Start with SSH Master tests only**
- Pros: Fast, stable, no Docker needed
- Cons: Won't catch v0.1.4 bug

**Option C: Run one E2E test as smoke test**
- Pros: Catches real bugs, reasonably fast
- Cons: More complex than SSH tests

**Recommended:** Option C - Run `TestManager_ContainerWithOnePort` as smoke test first, then enable all tests.

---

## Detailed Implementation Steps

### Step 1: Update Workflow Triggers

```yaml
name: Integration Tests

on:
  pull_request:
    branches: [ main ]
    paths:
      - '**.go'
      - 'go.mod'
      - 'go.sum'
      - '.github/workflows/integration-test.yml'
  workflow_dispatch:
    inputs:
      ssh_host:
        description: 'SSH test host (optional, uses localhost if not provided)'
        required: false
        default: 'localhost'
  schedule:
    - cron: '0 2 * * *'
```

**Rationale:** Only run integration tests when Go code or workflow changes.

### Step 2: Fix SSH Host Configuration

**Current SSH setup** (lines 33-67) needs adjustment:

```yaml
- name: Set up SSH test environment
  run: |
    # Install SSH server
    sudo apt-get update
    sudo apt-get install -y openssh-server
    
    # Start SSH service
    sudo systemctl start ssh
    
    # Create test user with Docker access
    sudo useradd -m -s /bin/bash testuser
    echo "testuser:testpass123" | sudo chpasswd
    sudo usermod -aG docker testuser
    
    # Generate SSH key for testuser
    sudo -u testuser ssh-keygen -t ed25519 -f /home/testuser/.ssh/id_ed25519 -N ""
    sudo -u testuser cat /home/testuser/.ssh/id_ed25519.pub >> /home/testuser/.ssh/authorized_keys
    sudo -u testuser chmod 600 /home/testuser/.ssh/authorized_keys
    
    # Configure SSH known_hosts
    sudo -u testuser ssh-keyscan -H localhost >> /home/testuser/.ssh/known_hosts
    
    # Test SSH connection
    sudo -u testuser ssh -o StrictHostKeyChecking=no testuser@localhost echo "SSH OK"
```

### Step 3: Set Environment Variables Correctly

```yaml
- name: Run integration tests
  env:
    SSH_TEST_HOST: ssh://testuser@localhost
    SSH_TEST_KEY_PATH: /home/testuser/.ssh/id_ed25519
  run: |
    # Run as testuser to use their SSH keys and Docker access
    sudo -u testuser -E bash -c '
      export HOME=/home/testuser
      export SSH_TEST_HOST=ssh://testuser@localhost
      go test -v -timeout=5m ./tests/integration/...
    '
```

### Step 4: Add Test Selection (Optional)

For faster feedback, run subset first:

```yaml
- name: Run SSH Master tests (fast)
  env:
    SSH_TEST_HOST: ssh://testuser@localhost
  run: |
    sudo -u testuser -E bash -c '
      export HOME=/home/testuser
      go test -v -timeout=2m -run TestSSHMaster ./tests/integration/
    '

- name: Run E2E smoke test
  env:
    SSH_TEST_HOST: ssh://testuser@localhost
  run: |
    sudo -u testuser -E bash -c '
      export HOME=/home/testuser
      go test -v -timeout=3m -run TestManager_ContainerWithOnePort ./tests/integration/
    '
```

---

## Testing the Changes

### Local Validation (Before PR)

```bash
# 1. Setup local SSH server (Ubuntu/Debian)
sudo apt-get install openssh-server
sudo systemctl start ssh

# 2. Create test user
sudo useradd -m -s /bin/bash testuser
echo "testuser:testpass123" | sudo chpasswd
sudo usermod -aG docker testuser

# 3. Setup SSH key
sudo -u testuser ssh-keygen -t ed25519 -f /home/testuser/.ssh/id_ed25519 -N ""
sudo -u testuser cat /home/testuser/.ssh/id_ed25519.pub >> /home/testuser/.ssh/authorized_keys
sudo -u testuser chmod 600 /home/testuser/.ssh/authorized_keys

# 4. Test SSH
sudo -u testuser ssh testuser@localhost echo "SSH works"

# 5. Run integration tests
sudo -u testuser -E bash -c '
  export HOME=/home/testuser
  export SSH_TEST_HOST=ssh://testuser@localhost
  cd /path/to/project
  go test -v ./tests/integration/...
'
```

### CI Validation (In PR)

1. Create PR with workflow changes
2. Observe GitHub Actions run
3. Check test results
4. Fix any failures
5. Merge when green

---

## Expected Test Results

### Tests That Should Pass

✅ **SSH Master Tests** (8 tests)
- Simple SSH operations
- No Docker required
- Fast (~5s total)

✅ **Container With No Ports** 
- Starts container, verifies graceful handling
- \~3s

### Tests That Might Need Adjustment

⚠️ **Container With One Port**
- Needs nginx container
- Needs port forwarding
- Might timeout if setup slow
- **This would catch v0.1.4 bug!**

⚠️ **Container With Three Ports**
- More complex
- Longer convergence time
- Might be flaky

### Potential Issues

1. **Docker permissions**
   ```
   Error: permission denied while trying to connect to Docker daemon
   ```
   **Fix:** Ensure `sudo -u testuser` is used

2. **SSH key issues**
   ```
   Error: Permission denied (publickey)
   ```
   **Fix:** Check key permissions, authorized_keys setup

3. **Port conflicts**
   ```
   Error: port 8080 already in use
   ```
   **Fix:** Use higher ports (18080+) or cleanup before tests

4. **Timeout issues**
   ```
   Error: context deadline exceeded
   ```
   **Fix:** Increase test timeout, check SSH connection

---

## Success Criteria

### Must Have
- [ ] Integration tests run on every PR that changes Go code
- [ ] At least SSH Master tests pass consistently
- [ ] Tests complete in <3 minutes
- [ ] Clear failure messages in PR checks
- [ ] No manual setup required

### Nice to Have
- [ ] All integration tests pass
- [ ] Tests run in <2 minutes
- [ ] Parallel test execution
- [ ] Test result summary in PR

### Would Catch v0.1.4 Bug
- [ ] `TestManager_ContainerWithOnePort` runs and passes
- [ ] Test verifies port forwarding works
- [ ] Test would fail if stream closes immediately

---

## Risk Assessment

### Low Risk
✅ Adding PR trigger - worst case, we can revert
✅ SSH Master tests - simple, stable
✅ Workflow changes - scoped to one file

### Medium Risk
⚠️ E2E tests might be flaky - can skip them initially
⚠️ Docker permissions - might need debugging
⚠️ Timing issues - can adjust timeouts

### High Risk
❌ None - all changes are reversible

---

## Rollout Plan

### Phase 1: PR Trigger + SSH Tests Only (Low Risk)
**Changes:**
- Add `pull_request` trigger
- Fix `SSH_TEST_HOST` format
- Run only `TestSSHMaster_*` tests

**Expected time:** ~30s test run
**Risk:** Very low

### Phase 2: Add One E2E Test (Medium Risk)
**Changes:**
- Add `TestManager_ContainerWithOnePort`

**Expected time:** +30s
**Risk:** Medium (might need debugging)

### Phase 3: Enable All Tests (Optional)
**Changes:**
- Run all integration tests

**Expected time:** ~2-3m
**Risk:** Medium (some might be flaky)

---

## Open Questions

1. **Test selection:** Run all tests or subset on PRs?
   - **Recommendation:** Start with SSH + one E2E test

2. **Timeout:** What's reasonable timeout for E2E tests?
   - **Recommendation:** 3m per test, 5m total

3. **Failure handling:** Block PR merge if tests fail?
   - **Recommendation:** Yes, tests are quality gate

4. **Performance:** Run tests in parallel?
   - **Recommendation:** Sequential first, optimize later

5. **Coverage:** Add test coverage reporting?
   - **Recommendation:** Out of scope for now

---

## Implementation Checklist

- [ ] Update `.github/workflows/integration-test.yml`
  - [ ] Add `pull_request` trigger
  - [ ] Fix `SSH_TEST_HOST` to `ssh://testuser@localhost`
  - [ ] Adjust test execution to run as testuser
  - [ ] Add clear step names and descriptions

- [ ] Test locally
  - [ ] Setup local SSH server
  - [ ] Run integration tests as testuser  
  - [ ] Verify all pass locally

- [ ] Create PR with changes
  - [ ] Include workflow updates
  - [ ] Document in PR description
  - [ ] Watch CI run

- [ ] Fix any CI failures
  - [ ] Debug SSH setup issues
  - [ ] Adjust timeouts if needed
  - [ ] Fix test flakiness

- [ ] Document in `docs/ci-cd.md`
  - [ ] How integration tests run
  - [ ] What they validate
  - [ ] How to debug failures

- [ ] Update `README.md` if needed
  - [ ] Mention CI integration tests
  - [ ] Link to docs

---

## Next Steps After Plan Review

1. **Approve plan** - User reviews and approves this approach
2. **Switch to code mode** - Implement workflow changes
3. **Test locally** - Verify changes work  
4. **Create PR** - Submit for CI validation
5. **Iterate** - Fix any issues that arise
6. **Document** - Update project documentation
7. **Monitor** - Watch tests run on future PRs
