# Tunnel Stability Tests - Implementation Task Plan

## Approved Configuration

✅ **Load Level**: 40 requests/second (4 workers × 10 req/s)  
✅ **Success Thresholds**: 95% (long-running), 98% (load test)  
✅ **Test Duration**: 3 minutes per test  
✅ **Worker Count**: 4 concurrent workers  
✅ **CI Strategy**: Part of regular test suite (runs on every PR)  
✅ **Execution**: Serial (to prevent interference)

---

## Implementation Tasks

### Phase 1: Helper Functions (Foundation)

#### Task 1.1: Create `tunnel_stability_helpers.go`
**File**: `tests/integration/tunnel_stability_helpers.go`

**Sub-tasks**:
- [ ] 1.1.1 Create `createLoggerWithCapture()` function
  - Multi-writer to stdout + buffer
  - Configurable log level
  - Return `*slog.Logger`
  
- [ ] 1.1.2 Create `setupManagerWithLogger()` function
  - Clone logic from existing `setupManager()`
  - Accept custom logger parameter
  - Same cleanup behavior

- [ ] 1.1.3 Create `getSSHMasterPID()` function
  - Use `ssh -O check` parsing
  - Extract PID from "Master running (pid=XXX)" output
  - Return -1 on error
  - Add test helper tag

- [ ] 1.1.4 Create `monitorSSHPIDChanges()` function
  - Background monitoring with 2s ticker
  - Record PID changes as `PIDChange` struct
  - Return slice of changes

- [ ] 1.1.5 Create `verifyNoReconnectionWarnings()` function
  - Regex patterns for reconnection indicators
  - Check for: "Permanently added", "event stream failed", etc.
  - Return list of issues found

**Acceptance Criteria**:
- All functions compile without errors
- Functions follow existing test helper patterns
- Proper error handling and logging
- Test helper tag (`t.Helper()`) used appropriately

**Estimated Time**: 2 hours

---

#### Task 1.2: Create Supporting Data Structures
**File**: `tests/integration/tunnel_stability_helpers.go`

**Sub-tasks**:
- [ ] 1.2.1 Define `TunnelStabilityStats` struct
  - Duration, expected requests, success/failure counts
  - Per-port stats map

- [ ] 1.2.2 Define `PortStats` struct
  - Request counts, latency tracking

- [ ] 1.2.3 Define `ReconnectionEvent` struct
  - Timestamp, old/new PID, elapsed time

- [ ] 1.2.4 Define `LoadStats` struct
  - Request stats, latency percentiles (min, max, avg, p95, p99)
  - Latencies slice for percentile calculation

- [ ] 1.2.5 Define `PIDChange` struct
  - Time, old/new PID

**Acceptance Criteria**:
- All structs properly documented
- Fields use appropriate types
- Follows Go naming conventions

**Estimated Time**: 30 minutes

---

### Phase 2: Load Generation Function

#### Task 2.1: Implement `generateLoad()` Function
**File**: `tests/integration/tunnel_stability_helpers.go`

**Sub-tasks**:
- [ ] 2.1.1 Create HTTP client with proper timeouts
  - 3-second timeout
  - Connection pooling (MaxIdleConnsPerHost)
  - Idle connection timeout

- [ ] 2.1.2 Implement worker pool pattern
  - Launch N goroutines (configurable)
  - Each worker has request ticker
  - Context cancellation support

- [ ] 2.1.3 Add request execution logic
  - HTTP GET to target port
  - Measure latency
  - Handle errors gracefully

- [ ] 2.1.4 Add thread-safe stats collection
  - Mutex-protected updates
  - Track success/failure counts
  - Store latencies for percentiles

- [ ] 2.1.5 Calculate final statistics
  - Sort latencies
  - Compute percentiles (median, p95, p99)
  - Calculate requests/second

**Acceptance Criteria**:
- Function handles concurrent workers correctly
- No race conditions (verify with `-race` flag)
- Accurate latency percentile calculation
- Clean shutdown on context cancellation

**Estimated Time**: 2 hours

---

### Phase 3: Test Implementation

#### Task 3.1: Implement `TestManager_LongRunningTunnelStability`
**File**: `tests/integration/tunnel_stability_test.go`

**Sub-tasks**:
- [ ] 3.1.1 Setup phase
  - Log capture setup
  - Manager initialization with custom logger
  - Wait for manager ready

- [ ] 3.1.2 Container startup (3 ports)
  - Start 3 nginx containers on ports 19081-19083
  - Store cleanup functions
  - Wait for all tunnels to establish

- [ ] 3.1.3 Initial state capture
  - Get SSH ControlMaster PID
  - Log initial state

- [ ] 3.1.4 Continuous testing loop (3 minutes)
  - Ticker-based request generation (5s intervals)
  - Round-robin port testing
  - Per-port stats tracking
  - SSH PID monitoring
  - Detect reconnections

- [ ] 3.1.5 Results calculation and display
  - Per-port statistics
  - Overall success rate
  - Reconnection count
  - Log output formatting

- [ ] 3.1.6 Log verification
  - Call `verifyNoReconnectionWarnings()`
  - Display any issues found

- [ ] 3.1.7 Assertions
  - No SSH reconnections
  - >95% overall success rate
  - >95% per-port success rate
  - No log issues
  - Minimum request count met

**Acceptance Criteria**:
- Test runs for full 3 minutes
- All 3 ports tested evenly
- Clear progress logging
- Comprehensive result display
- Proper cleanup on failure

**Estimated Time**: 3 hours

---

#### Task 3.2: Implement `TestManager_TunnelStabilityUnderLoad`
**File**: `tests/integration/tunnel_stability_test.go`

**Sub-tasks**:
- [ ] 3.2.1 Setup phase
  - Log capture setup
  - Manager initialization
  - Single container on port 19090

- [ ] 3.2.2 Tunnel establishment
  - Wait for port to open
  - Get initial SSH PID

- [ ] 3.2.3 Launch load generation
  - Call `generateLoad()` with 4 workers
  - 10 req/s per worker (100ms interval)
  - 3-minute duration
  - Target port 19090

- [ ] 3.2.4 Background SSH monitoring
  - Run `monitorSSHPIDChanges()` concurrently
  - Detect any reconnections during load

- [ ] 3.2.5 Results display
  - Load statistics (requests, success rate)
  - Latency statistics (min, max, avg, p95, p99)
  - SSH stability report

- [ ] 3.2.6 Log verification
  - Check for reconnection warnings

- [ ] 3.2.7 Assertions
  - >98% success rate
  - No SSH reconnections
  - P95 latency <500ms
  - Minimum request count (~6,400 = 90% of 7,200)
  - No log issues

**Acceptance Criteria**:
- Generates ~7,200 requests over 3 minutes
- Accurate latency percentile calculation
- No race conditions in concurrent execution
- Clear reporting of results

**Estimated Time**: 2.5 hours

---

### Phase 4: Integration and Testing

#### Task 4.1: Local Testing
**Sub-tasks**:
- [ ] 4.1.1 Test with current code (should fail)
  - Start test harness: `make itest-up`
  - Run tests: `go test -v -timeout=10m -run TestManager_.*Stability`
  - Verify tests detect issues (if present)
  - Document failure patterns

- [ ] 4.1.2 Test with fixed code
  - Apply the 5 fixes from event-stream-failure-fix-deployment.md
  - Run tests again
  - Verify tests pass
  - Document success patterns

- [ ] 4.1.3 Verify with `-race` flag
  - Run: `go test -race -v -timeout=10m -run TestManager_.*Stability`
  - Fix any race conditions
  - Ensure clean execution

**Acceptance Criteria**:
- Tests clearly show before/after difference
- No race conditions detected
- Tests complete in expected time (~7 min total)

**Estimated Time**: 2 hours

---

#### Task 4.2: Update Test Configuration
**Files**: `Makefile`, test documentation

**Sub-tasks**:
- [ ] 4.2.1 Update Makefile test targets
  - Add `itest-stability` target for just stability tests
  - Update `itest` to include stability tests
  - Document serial execution requirement

- [ ] 4.2.2 Update test documentation
  - Add stability tests to [`docs/ci-integration-tests.md`](../docs/ci-integration-tests.md)
  - Document expected runtime impact
  - Add troubleshooting section

- [ ] 4.2.3 Add test skip logic
  - Skip in short mode: `if testing.Short()`
  - Skip if SSH not available
  - Clear skip messages

**Acceptance Criteria**:
- `make itest` runs all tests including stability
- `make itest-stability` runs only stability tests
- Documentation is clear and complete

**Estimated Time**: 1 hour

---

#### Task 4.3: CI Workflow Integration
**File**: `.github/workflows/ci.yml` (or equivalent)

**Sub-tasks**:
- [ ] 4.3.1 Verify serial execution
  - Confirm tests don't run in parallel
  - Check for `-p 1` flag if needed

- [ ] 4.3.2 Update timeout
  - Increase test timeout to accommodate 7-minute runtime
  - Add buffer for CI overhead (suggest 15-minute timeout)

- [ ] 4.3.3 Add test result reporting
  - Ensure stability test failures are clearly visible
  - Consider separate status check for stability tests

**Acceptance Criteria**:
- Tests run serially in CI
- CI doesn't timeout prematurely
- Test results clearly reported

**Estimated Time**: 1 hour

---

### Phase 5: Documentation and Polish

#### Task 5.1: Code Documentation
**Sub-tasks**:
- [ ] 5.1.1 Add comprehensive godoc comments
  - All exported functions
  - Test function descriptions
  - Parameter explanations

- [ ] 5.1.2 Add inline code comments
  - Explain non-obvious logic
  - Document design decisions
  - Reference relevant issues/PRs

**Estimated Time**: 1 hour

---

#### Task 5.2: Test Output Polish
**Sub-tasks**:
- [ ] 5.2.1 Improve progress logging
  - Clear section headers
  - Progress indicators
  - Summary tables

- [ ] 5.2.2 Add color/formatting (if appropriate)
  - Use testing.T.Log for structure
  - Consider ASCII tables for results

**Estimated Time**: 30 minutes

---

#### Task 5.3: Update Documentation
**Files**: CONTRIBUTING.md, README.md, troubleshooting guide

**Sub-tasks**:
- [ ] 5.3.1 Update CONTRIBUTING.md
  - Add stability test section
  - Explain when/how to run

- [ ] 5.3.2 Update troubleshooting guide
  - Add section on stability test failures
  - Common issues and solutions

- [ ] 5.3.3 Update CI/CD documentation
  - Note increased runtime
  - Explain purpose of stability tests

**Estimated Time**: 1 hour

---

## Task Summary

| Phase | Tasks | Estimated Time | Priority |
|-------|-------|----------------|----------|
| **Phase 1: Helpers** | 1.1, 1.2 | 2.5 hours | High |
| **Phase 2: Load Gen** | 2.1 | 2 hours | High |
| **Phase 3: Tests** | 3.1, 3.2 | 5.5 hours | Critical |
| **Phase 4: Integration** | 4.1, 4.2, 4.3 | 4 hours | High |
| **Phase 5: Docs** | 5.1, 5.2, 5.3 | 2.5 hours | Medium |
| **Total** | 13 tasks | **16.5 hours** | - |

---

## Implementation Order (Recommended)

### Sprint 1: Foundation (4.5 hours)
1. Task 1.1: Helper functions ✅ Foundation
2. Task 1.2: Data structures ✅ Foundation
3. Task 2.1: Load generation ✅ Core functionality

### Sprint 2: Core Tests (5.5 hours)
4. Task 3.1: Long-running stability test ✅ Primary test
5. Task 3.2: Load test ✅ Secondary test

### Sprint 3: Integration (4 hours)
6. Task 4.1: Local testing ✅ Validation
7. Task 4.2: Test configuration ✅ Infrastructure
8. Task 4.3: CI integration ✅ Automation

### Sprint 4: Polish (2.5 hours)
9. Task 5.1: Code documentation ✅ Quality
10. Task 5.2: Test output ✅ UX
11. Task 5.3: Documentation ✅ Completeness

---

## Risk Mitigation

### Risk 1: Race Conditions
**Mitigation**: 
- Always run with `-race` flag during development
- Use proper mutex protection for shared stats
- Test locally before CI

### Risk 2: CI Timeout
**Mitigation**:
- Set conservative timeout (15 min)
- Add progress logging
- Consider splitting tests if needed

### Risk 3: Flaky Tests
**Mitigation**:
- Use appropriate success rate thresholds (95%, 98%)
- Handle network hiccups gracefully
- Retry PID detection if initial attempt fails

### Risk 4: Resource Contention in CI
**Mitigation**:
- Serial execution (approved)
- Reasonable load levels (40 req/s)
- Monitor CI resource usage

---

## Success Criteria

### For Each Test:
- ✅ Runs reliably in local environment
- ✅ Passes after fixes are applied
- ✅ Fails before fixes (validates test effectiveness)
- ✅ No race conditions detected
- ✅ Clear, actionable output
- ✅ Completes within expected time

### For Overall Implementation:
- ✅ Integrated into regular test suite
- ✅ CI passes consistently
- ✅ Documentation is complete
- ✅ Code is well-commented
- ✅ No performance regression in other tests

---

## Testing Checklist

Before marking complete:
- [ ] Run locally with current code (should fail/detect issues)
- [ ] Run locally with fixes applied (should pass)
- [ ] Run with `-race` flag (no races)
- [ ] Run in CI environment
- [ ] Verify test output is clear
- [ ] Check documentation is accurate
- [ ] Confirm CI timeout is adequate
- [ ] Verify tests don't interfere with each other

---

## Questions for Review

1. **Task Granularity**: Are the tasks broken down appropriately? Too detailed or not enough?

2. **Estimated Times**: Do the time estimates seem reasonable? (Total: 16.5 hours)

3. **Implementation Order**: Makes sense to do helpers → tests → integration → polish?

4. **Priority Levels**: Agree with High priority for Phases 1-4, Medium for Phase 5?

5. **Success Criteria**: Are the defined success criteria sufficient?

6. **Risk Mitigation**: Any other risks we should plan for?

7. **Sprint Division**: The 4-sprint breakdown works, or prefer different grouping?

---

## Next Steps After Approval

1. Create GitHub issue/task tracking
2. Start with Phase 1 (helpers)
3. Commit incrementally after each task
4. Run tests after each phase
5. Update documentation as we go
6. Final PR with all changes

Ready to proceed with implementation?