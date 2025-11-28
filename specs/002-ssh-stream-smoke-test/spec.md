# Spec 002: SSH Stream Persistence Smoke Test

**Status:** Planning  
**Created:** 2025-11-11  
**Author:** System  
**Priority:** High (addresses production bug in v0.1.4)

## Problem Statement

### The Bug We Missed

In v0.1.4, our unit tests successfully caught the **shell quoting syntax bug** but completely missed the **stream persistence runtime bug**:

```
✅ Unit test passed: Command syntax looks correct
❌ Production failed: Stream closes after 38ms instead of staying open
```

**Root Cause of Testing Gap:**
- Unit tests verify command string formation
- Unit tests use mocked/cancelled contexts (1ms timeout)
- No tests actually execute SSH commands over ControlMaster
- No tests verify long-running stream behavior

### Impact

**v0.1.4 Production Failure:**
```
time=... msg="docker events stream started"
time=... msg="docker events stream ended"  # 38ms later!
time=... msg="event error channel closed"
time=... msg="manager event loop started"  # Infinite restart loop
```

**Result:** Zero containers detected, zero port forwards created, application unusable.

---

## Current Testing Landscape

### What We Have

```
Unit Tests (50+ tests)
├── Command syntax validation ✅
├── Struct creation ✅
├── Error handling ✅
└── Mock-based logic ✅

Integration Tests (6 tests in tests/integration/)
├── SSH ControlMaster lifecycle ⚠️ Skipped if SSH_TEST_HOST not set
├── Container lifecycle ⚠️ Skipped if SSH_TEST_HOST not set
└── End-to-end scenarios ⚠️ Skipped if SSH_TEST_HOST not set

GitHub Actions
├── Lint ✅ Runs on every PR
├── Unit tests ✅ Runs on every PR
└── Integration tests ❌ Manual workflow only (not on PR)
```

### The Gap

**No automated tests verify:**
1. SSH commands execute successfully over ControlMaster
2. Long-running streams stay open (not just start)
3. Docker events stream behavior in CI environment
4. Real SSH + real Docker integration

---

## Proposed Solution

### Architecture Overview

```
┌─────────────────────────────────────────────────────────┐
│ GitHub Runner (Ubuntu)                                  │
│                                                         │
│  ┌──────────────┐         SSH           ┌────────────┐ │
│  │  Smoke Test  │────────────────────────▶   sshd    │ │
│  │              │  ControlMaster         │  (local)  │ │
│  │              │◀────────────────────────│           │ │
│  └──────────────┘                        └────────────┘ │
│         │                                       │        │
│         │ Starts & monitors                    │        │
│         ▼                                       │        │
│  ┌──────────────┐                              │        │
│  │ Docker Events│──────────────────────────────┘        │
│  │    Stream    │  docker events --format '{{json .}}' │
│  │              │  via SSH over ControlMaster           │
│  └──────────────┘                                       │
│         │                                                │
│         │ Verifies stream stays open 5+ seconds         │
│         ▼                                                │
│     ✅ PASS or ❌ FAIL                                   │
│                                                          │
└──────────────────────────────────────────────────────────┘
```

### Test Design: Single Focused Smoke Test

**File:** `tests/integration/docker_events_stream_smoke_test.go`

**Purpose:** Verify Docker events stream persists when using SSH ControlMaster

**Test Case:** `TestDockerEventsStreamPersistence_ControlMaster`

**Test Flow:**
1. Setup SSH ControlMaster to localhost
2. Start Docker events stream via ControlMaster
3. Verify stream doesn't close immediately (runs ≥5s)
4. Verify stream can be cancelled cleanly
5. Cleanup

**Expected Behavior:**
```go
// ✅ Expected (correct behavior)
Stream starts → runs for 5+ seconds → test cancels → stream ends → PASS

// ❌ v0.1.4 Bug (what we're catching)
Stream starts → closes in <100ms → FAIL with clear error message
```

---

## Implementation Plan

### Test Implementation

**Location:** `tests/integration/docker_events_stream_smoke_test.go`

**Code Structure:**
```go
// TestDockerEventsStreamPersistence_ControlMaster verifies that docker events
// stream stays open over SSH ControlMaster (smoke test for v0.1.4 bug)
func TestDockerEventsStreamPersistence_ControlMaster(t *testing.T) {
    // 1. Get SSH test host (skips if not set)
    sshHost := getTestSSHHost(t)
    
    // 2. Setup SSH ControlMaster
    master := setupSSHMaster(t, sshHost)
    defer master.Close()
    
    // 3. Start Docker events stream
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    
    reader := docker.NewEventReader(sshHost, master.ControlPath(), logger)
    events, errs := reader.Stream(ctx)
    
    // 4. Critical assertion: Stream should NOT close immediately
    select {
    case <-events:
        t.Fatal("❌ Stream closed immediately - v0.1.4 bug detected!")
    case err := <-errs:
        t.Fatalf("❌ Stream errored immediately: %v", err)
    case <-time.After(5 * time.Second):
        // ✅ Stream stayed open for 5s - PASS
        t.Log("✅ Stream persisted for 5+ seconds")
    }
    
    // 5. Verify clean cancellation
    cancel()
    
    select {
    case <-events:
        t.Log("✅ Stream closed cleanly after cancellation")
    case <-time.After(2 * time.Second):
        t.Error("⚠️ Stream didn't close within 2s after cancel")
    }
}
```

**Key Features:**
- **Fails fast:** If stream closes in <5s, test fails immediately with clear message
- **Self-contained:** Uses existing test infrastructure (getTestSSHHost helper)
- **Clear diagnostics:** Logs show exactly what failed and when
- **Timeout protection:** Won't hang forever if stream never starts

### GitHub Actions Integration

**Approach:** Enhance existing `.github/workflows/integration-test.yml`

**Changes Required:**

1. **Add smoke test job** (runs on every PR, before full integration tests)

```yaml
jobs:
  smoke-test:
    name: SSH Stream Smoke Test
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      
      - name: Setup SSH to localhost
        run: |
          # Install SSH server
          sudo apt-get update && sudo apt-get install -y openssh-server
          sudo systemctl start ssh
          
          # Configure passwordless auth
          ssh-keygen -t ed25519 -f ~/.ssh/id_test -N ""
          cat ~/.ssh/id_test.pub >> ~/.ssh/authorized_keys
          chmod 600 ~/.ssh/authorized_keys
          
          # Test connection
          ssh -i ~/.ssh/id_test -o StrictHostKeyChecking=no \
              $(whoami)@localhost echo "SSH ready"
      
      - name: Run smoke test
        env:
          SSH_TEST_HOST: ssh://$(whoami)@localhost
        run: |
          go test -v -timeout=30s \
            -run TestDockerEventsStreamPersistence \
            ./tests/integration/
```

2. **Make integration tests depend on smoke test**

```yaml
  integration-test:
    name: Full Integration Tests
    needs: smoke-test  # Only run if smoke test passes
    runs-on: ubuntu-latest
    # ... rest of existing config
```

**Benefits:**
- Smoke test runs on **every PR** (catches bugs early)
- Fast feedback (~30s total runtime)
- Full integration tests only run if smoke test passes
- Clear failure indication in PR checks

---

## Test Infrastructure: GitHub Actions SSH Setup

### How We Create SSH Test Environment

**Option 1: Localhost SSH (Recommended)**
- GitHub runners have SSH server available
- We just need to enable it and configure keys
- Fast, reliable, no external dependencies
- Already partially implemented in existing workflow

**Option 2: Docker-based SSH Container**
- Use existing `docker/sshd-stub/Dockerfile`
- More complex setup
- Useful for advanced scenarios
- Not needed for basic smoke test

**Choice:** Use Option 1 (localhost SSH) for smoke test

### SSH Setup Steps (in GitHub Actions)

```bash
# 1. Install SSH server (already on runner, just ensure it's running)
sudo systemctl start ssh

# 2. Generate test key
ssh-keygen -t ed25519 -f ~/.ssh/id_test -N ""

# 3. Configure passwordless auth
cat ~/.ssh/id_test.pub >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys

# 4. Add to known_hosts
ssh-keyscan localhost >> ~/.ssh/known_hosts

# 5. Test connection
ssh -i ~/.ssh/id_test $(whoami)@localhost echo "Ready"
```

**Environment Variable:**
```bash
export SSH_TEST_HOST="ssh://$(whoami)@localhost"
```

This makes all existing integration tests work with zero code changes!

---

## Success Criteria

### Test Must Catch

1. ✅ **v0.1.4 bug:** Stream closing immediately
2. ✅ **Shell expansion:** Template syntax issues
3. ✅ **ControlMaster issues:** Connection multiplexing failures
4. ✅ **Timeout issues:** Stream hanging forever

### Test Must NOT

1. ❌ Require manual setup
2. ❌ Require external services
3. ❌ Take >1 minute to run
4. ❌ Be flaky (timing-dependent)

### Acceptance Criteria

- [ ] Test runs in GitHub Actions on every PR
- [ ] Test completes in <30 seconds
- [ ] Test would have caught v0.1.4 bug (verified by testing against v0.1.4 code)
- [ ] Test passes with v0.1.5+ code (after fix)
- [ ] Test has clear failure messages
- [ ] Zero false positives in 10 consecutive runs

---

## Testing the Test

### Verification Plan

**Phase 1: Test Against v0.1.4 (Should FAIL)**
```bash
# Checkout v0.1.4 code
git checkout v0.1.4

# Run smoke test
go test -v -run TestDockerEventsStreamPersistence ./tests/integration/

# Expected: ❌ FAIL with "Stream closed immediately"
```

**Phase 2: Test Against Fixed Code (Should PASS)**
```bash
# Checkout fixed code
git checkout fix/stream-persistence

# Run smoke test
go test -v -run TestDockerEventsStreamPersistence ./tests/integration/

# Expected: ✅ PASS with "Stream persisted for 5+ seconds"
```

**Phase 3: CI Integration**
```bash
# Create PR with smoke test
# Observe GitHub Actions

# Expected:
# - Smoke test job runs first
# - Completes in ~30s
# - Shows green checkmark if passing
# - Blocks merge if failing
```

---

## Implementation Checklist

### Phase 1: Test Implementation
- [ ] Create `tests/integration/docker_events_stream_smoke_test.go`
- [ ] Implement `TestDockerEventsStreamPersistence_ControlMaster`
- [ ] Add helper function `setupSSHMaster` if needed
- [ ] Test locally with `SSH_TEST_HOST=ssh://user@localhost`
- [ ] Verify test FAILS against v0.1.4 code
- [ ] Verify test PASSES against fixed code

### Phase 2: CI Integration
- [ ] Update `.github/workflows/integration-test.yml`
- [ ] Add `smoke-test` job with localhost SSH setup
- [ ] Configure job dependencies (smoke → integration)
- [ ] Add clear job description and outputs
- [ ] Test workflow in PR

### Phase 3: Documentation
- [ ] Update `docs/ci-cd.md` with smoke test description
- [ ] Add troubleshooting section for SSH setup issues
- [ ] Document how to run smoke test locally
- [ ] Add diagram of test architecture

### Phase 4: Validation
- [ ] Run smoke test 10 times locally (check for flakiness)
- [ ] Run smoke test in GitHub Actions (verify setup)
- [ ] Check timing (must be <30s)
- [ ] Verify failure messages are clear
- [ ] Get code review

---

## Future Enhancements (Out of Scope)

**Not included in this spec, but could be added later:**

1. **Multi-platform testing:** Run smoke test on macOS runners
2. **Performance benchmarking:** Measure stream latency
3. **Event injection:** Trigger actual Docker events during test
4. **Parallel execution:** Run multiple streams simultaneously
5. **Remote host testing:** Add workflow for testing against remote SSH hosts

**Rationale for exclusion:** We want ONE focused smoke test that catches the v0.1.4 bug. Additional features can be added incrementally based on need.

---

## Estimated Implementation Time

**Not providing time estimates as requested, but here's the complexity breakdown:**

- **Test code:** Simple (reuses existing patterns)
- **CI setup:** Medium (SSH configuration in Actions)
- **Documentation:** Simple (update existing docs)
- **Testing the test:** Medium (need to verify against v0.1.4)

**Complexity Level:** Medium (mostly CI configuration work)

---

## Questions for Review

1. **Timing:** Is 5 seconds the right threshold? (We could use 3s or 10s)
2. **CI trigger:** Should smoke test run on every push or only on PR?
3. **SSH method:** Localhost SSH vs Docker container - is localhost sufficient?
4. **Test name:** Is the name clear enough? Alternative: `TestEventStreamDoesNotCloseImmediately`
5. **Placement:** Should this be in `tests/integration/` or new `tests/smoke/`?

---

## Appendix: Test Comparison

### Unit Test (Current)
```go
// What it tests
✅ events.NewEventReader() returns correct type
✅ Command string contains "sh -c"
✅ Command string contains '{{json .}}'

// What it misses
❌ Does command actually execute?
❌ Does stream stay open?
❌ Does ControlMaster work?
```

### Smoke Test (Proposed)
```go
// What it tests
✅ Command executes successfully
✅ Stream stays open (5+ seconds)
✅ ControlMaster multiplexing works
✅ Real SSH + real Docker

// What it doesn't test (left for full integration tests)
⚠️ Event parsing
⚠️ Port forwarding
⚠️ Container lifecycle
```

### Full Integration Test (Existing)
```go
// What it tests
✅ Complete end-to-end flow
✅ Container start → event → forward → HTTP test
✅ Multiple containers
✅ Cleanup

// Why it's not enough alone
❌ Takes 30-60s to run
❌ Lots of moving parts (harder to debug)
❌ Only runs manually (not on every PR)
```

**Conclusion:** We need all three layers for comprehensive coverage.