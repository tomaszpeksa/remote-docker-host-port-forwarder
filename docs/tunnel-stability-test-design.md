# Tunnel Stability Test Design

## Overview

Design for integration tests that verify SSH tunnel stability over extended periods (3+ minutes) without reconnections. These tests validate the ControlMaster persistence fixes.

---

## Test Infrastructure (Already Available)

### Existing Setup
- **SSH Container**: Alpine-based SSHD on port 2222 ([`scripts/itest-up.sh`](../scripts/itest-up.sh))
- **Docker Access**: Host Docker socket mounted (`/var/run/docker.sock`)
- **Authentication**: SSH key-based (`.itests/home/.ssh/id_ed25519`)
- **Network**: Bridge networking (containers publish to `172.17.0.1`)
- **Test User**: `testuser` with Docker group membership

### Existing Test Patterns
- [`end_to_end_test.go`](../tests/integration/end_to_end_test.go): Basic forwarding tests (<30s)
- [`docker_events_stream_test.go`](../tests/integration/docker_events_stream_test.go): Stream persistence (5s)
- Helper functions: `setupManager()`, `startDockerContainer()`, `waitForPortOpen()`, etc.

---

## Test Case 1: Long-Running Tunnel Stability Test

### Purpose
Verify that an SSH tunnel remains stable for 3 minutes with continuous traffic, detecting any reconnections or ControlMaster failures.

### Test Name
`TestManager_LongRunningTunnelStability`

### Duration
~3.5 minutes (3min container + 30s setup/teardown)

### Test Flow

```go
func TestManager_LongRunningTunnelStability(t *testing.T) {
    // 1. Setup
    sshHost := getTestSSHHost(t)
    ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
    defer cancel()
    
    // 2. Start manager with diagnostic logging enabled
    mgr := setupManager(t, ctx, sshHost)
    waitForManagerReady(t, 2*time.Second)
    
    // 3. Start long-running HTTP container (nginx)
    testPort := 19080
    containerID, cleanup := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{
        testPort: 80,
    })
    defer cleanup()
    
    // 4. Wait for tunnel establishment
    require.True(t, waitForPortOpen(testPort, 5*time.Second), 
        "Tunnel should establish within 5s")
    
    // 5. Get initial SSH process PID (via control socket)
    initialPID := getSSHMasterPID(t, sshHost)
    t.Logf("Initial SSH ControlMaster PID: %d", initialPID)
    
    // 6. Continuously test connectivity for 3 minutes
    testDuration := 3 * time.Minute
    testInterval := 5 * time.Second
    
    startTime := time.Now()
    requestCount := 0
    failureCount := 0
    reconnectionDetected := false
    
    for time.Since(startTime) < testDuration {
        // Test HTTP connectivity
        statusCode, body, err := testHTTPConnection(testPort)
        
        if err != nil {
            failureCount++
            t.Logf("❌ Request %d failed after %v: %v", 
                requestCount+1, time.Since(startTime), err)
        } else {
            requestCount++
            // Verify response quality
            assert.Equal(t, 200, statusCode, "Should get 200 OK")
            assert.Contains(t, body, "nginx", "Response should be valid")
            
            if requestCount%5 == 0 {
                t.Logf("✅ Request %d succeeded after %v", 
                    requestCount, time.Since(startTime))
            }
        }
        
        // Check if SSH ControlMaster PID changed (indicates reconnection)
        currentPID := getSSHMasterPID(t, sshHost)
        if currentPID != initialPID {
            reconnectionDetected = true
            t.Errorf("❌ SSH ControlMaster reconnected! Initial PID: %d, Current PID: %d, Time: %v",
                initialPID, currentPID, time.Since(startTime))
            initialPID = currentPID // Update for subsequent checks
        }
        
        time.Sleep(testInterval)
    }
    
    totalDuration := time.Since(startTime)
    
    // 7. Assertions
    t.Logf("\n=== Test Results ===")
    t.Logf("Duration: %v", totalDuration)
    t.Logf("Total requests: %d", requestCount)
    t.Logf("Failed requests: %d", failureCount)
    t.Logf("Success rate: %.1f%%", float64(requestCount-failureCount)/float64(requestCount)*100)
    
    assert.False(t, reconnectionDetected, "SSH ControlMaster should not reconnect")
    assert.Equal(t, 0, failureCount, "All HTTP requests should succeed")
    assert.GreaterOrEqual(t, requestCount, 30, "Should complete ~36 requests (3min / 5s)")
    
    // 8. Verify no diagnostic warnings in logs
    // This would require log capture, but conceptually:
    // assert.NotContains(t, capturedLogs, "Permanently added")
    // assert.NotContains(t, capturedLogs, "event stream failed")
}
```

### Success Criteria
- ✅ All HTTP requests succeed (100% success rate)
- ✅ SSH ControlMaster PID remains constant (no reconnections)
- ✅ Tunnel remains responsive for full 3 minutes
- ✅ At least 30 successful requests (3min ÷ 5s intervals)

### Failure Indicators
- ❌ SSH ControlMaster PID changes (reconnection detected)
- ❌ HTTP requests fail or timeout
- ❌ Port becomes unreachable during test
- ❌ Less than 30 requests completed

---

## Test Case 2: Multi-Port Tunnel Stability Test

### Purpose
Verify that multiple simultaneous tunnels remain stable without interfering with each other.

### Test Name
`TestManager_MultiPortTunnelStability`

### Duration
~3.5 minutes

### Key Differences from Test 1
- **3 simultaneous tunnels** (ports 19081, 19082, 19083)
- **Round-robin testing** of all 3 ports
- **Verify all PIDs remain constant** (not just one)
- **Detect if one tunnel fails while others work**

### Test Flow (Abbreviated)

```go
func TestManager_MultiPortTunnelStability(t *testing.T) {
    // Setup 3 containers with different ports
    ports := []int{19081, 19082, 19083}
    
    // Start manager and containers
    // ...
    
    // Get initial SSH master PID (shared across all tunnels)
    initialPID := getSSHMasterPID(t, sshHost)
    
    // Test all 3 ports in rotation for 3 minutes
    testDuration := 3 * time.Minute
    portIdx := 0
    
    for time.Since(startTime) < testDuration {
        port := ports[portIdx]
        
        // Test connectivity on current port
        err := testTCPConnection(port)
        // Track per-port success rates
        
        // Check SSH PID (same for all tunnels)
        currentPID := getSSHMasterPID(t, sshHost)
        // Detect reconnections
        
        portIdx = (portIdx + 1) % len(ports)
        time.Sleep(2 * time.Second) // Faster rotation
    }
    
    // Assert all ports maintained stable tunnels
}
```

### Success Criteria
- ✅ All 3 ports remain accessible for full 3 minutes
- ✅ Single SSH ControlMaster serves all tunnels
- ✅ Per-port success rate >98%
- ✅ No reconnections detected

---

## Test Case 3: Tunnel Stability Under Load

### Purpose
Verify tunnel stability when handling continuous high-frequency requests.

### Test Name
`TestManager_TunnelStabilityUnderLoad`

### Duration
~3.5 minutes

### Load Pattern
- **Request frequency**: Every 100ms (10 req/sec)
- **Parallel requests**: 2 concurrent goroutines
- **Total requests**: ~3600 over 3 minutes
- **Request size**: Moderate (download ~10KB per request)

### Test Flow (Abbreviated)

```go
func TestManager_TunnelStabilityUnderLoad(t *testing.T) {
    // Setup
    // ...
    
    // Launch 2 concurrent request generators
    var wg sync.WaitGroup
    var mu sync.Mutex
    successCount := 0
    failureCount := 0
    
    for i := 0; i < 2; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            
            for time.Since(startTime) < testDuration {
                statusCode, _, err := testHTTPConnection(testPort)
                
                mu.Lock()
                if err == nil && statusCode == 200 {
                    successCount++
                } else {
                    failureCount++
                }
                mu.Unlock()
                
                time.Sleep(100 * time.Millisecond)
            }
        }(i)
    }
    
    // Monitor SSH PID in main goroutine
    reconnections := 0
    for time.Since(startTime) < testDuration {
        currentPID := getSSHMasterPID(t, sshHost)
        if currentPID != initialPID {
            reconnections++
            initialPID = currentPID
        }
        time.Sleep(2 * time.Second)
    }
    
    wg.Wait()
    
    // Assert high success rate under load
    totalRequests := successCount + failureCount
    successRate := float64(successCount) / float64(totalRequests) * 100
    
    assert.Equal(t, 0, reconnections, "No reconnections under load")
    assert.GreaterOrEqual(t, successRate, 98.0, "Success rate should be >98%")
}
```

### Success Criteria
- ✅ >98% success rate under load (>3500 successful requests)
- ✅ No SSH reconnections
- ✅ No connection timeouts
- ✅ Latency remains reasonable (<500ms p95)

---

## Helper Function: SSH Master PID Detection

### Implementation

```go
// getSSHMasterPID returns the PID of the SSH ControlMaster process
// Returns -1 if not found or error
func getSSHMasterPID(t *testing.T, sshHost string) int {
    t.Helper()
    
    controlPath, err := ssh.DeriveControlPath(sshHost)
    if err != nil {
        t.Logf("Failed to derive control path: %v", err)
        return -1
    }
    
    // Check if socket exists
    if _, err := os.Stat(controlPath); os.IsNotExist(err) {
        t.Logf("Control socket does not exist: %s", controlPath)
        return -1
    }
    
    // Use lsof to find PID listening on the socket
    // lsof returns: "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME"
    cmd := exec.Command("lsof", "-t", controlPath)
    output, err := cmd.Output()
    if err != nil {
        t.Logf("lsof failed: %v", err)
        return -1
    }
    
    pidStr := strings.TrimSpace(string(output))
    if pidStr == "" {
        return -1
    }
    
    pid, err := strconv.Atoi(pidStr)
    if err != nil {
        t.Logf("Failed to parse PID: %v", err)
        return -1
    }
    
    return pid
}

// Alternative implementation using SSH -O check
func getSSHMasterPIDViaCheck(t *testing.T, sshHost string) int {
    t.Helper()
    
    controlPath, err := ssh.DeriveControlPath(sshHost)
    if err != nil {
        return -1
    }
    
    sshHostClean, port, err := ssh.ParseHost(sshHost)
    if err != nil {
        return -1
    }
    
    // SSH -O check outputs: "Master running (pid=12345)"
    args := []string{"-S", controlPath, "-O", "check"}
    if port != "" {
        args = append(args, "-p", port)
    }
    args = append(args, sshHostClean)
    
    cmd := exec.Command("ssh", args...)
    output, err := cmd.CombinedOutput()
    if err != nil {
        return -1
    }
    
    // Parse "Master running (pid=12345)"
    re := regexp.MustCompile(`pid=(\d+)`)
    matches := re.FindStringSubmatch(string(output))
    if len(matches) < 2 {
        return -1
    }
    
    pid, _ := strconv.Atoi(matches[1])
    return pid
}
```

---

## Test Case 4: Reconnection Detection via Logs

### Purpose
Capture and validate diagnostic logs during tunnel operation to detect any "Permanently added..." warnings that indicate new SSH sessions.

### Test Name
`TestManager_NoReconnectionWarnings`

### Duration
~3.5 minutes

### Implementation Approach

```go
func TestManager_NoReconnectionWarnings(t *testing.T) {
    // 1. Setup log capture
    var logBuf bytes.Buffer
    logger := logging.NewLoggerWithWriter("debug", &logBuf)
    
    // 2. Create manager with captured logger
    // ... (same setup as Test 1)
    
    // 3. Run for 3 minutes with periodic connectivity tests
    // ...
    
    // 4. After completion, analyze logs
    logOutput := logBuf.String()
    
    // Search for reconnection indicators
    reconnectionPatterns := []string{
        "Permanently added",
        "event stream failed after",
        "SSH ControlMaster is dead, recreating",
        "consecutive_failures",
    }
    
    for _, pattern := range reconnectionPatterns {
        if strings.Contains(logOutput, pattern) {
            t.Errorf("❌ Found reconnection indicator in logs: %q", pattern)
            
            // Extract and log the problematic lines
            lines := strings.Split(logOutput, "\n")
            for _, line := range lines {
                if strings.Contains(line, pattern) {
                    t.Logf("  %s", line)
                }
            }
        }
    }
    
    // Verify positive indicators
    assert.Contains(t, logOutput, "DIAGNOSTIC: SSH ControlMaster verified healthy",
        "Should have health check confirmations")
    assert.Contains(t, logOutput, "docker events stream started",
        "Should have stream start log")
    
    // Should NOT see stream restarts (indicates stability)
    streamStarts := strings.Count(logOutput, "DIAGNOSTIC: Starting Docker events stream")
    assert.Equal(t, 1, streamStarts, "Should only start stream once (no restarts)")
}
```

### Success Criteria
- ✅ No "Permanently added..." warnings
- ✅ No "event stream failed" errors
- ✅ No "SSH ControlMaster is dead" messages
- ✅ Only 1 "Starting Docker events stream" message
- ✅ Multiple "ControlMaster verified healthy" confirmations

---

## Continuous Integration Setup

### GitHub Actions Workflow Addition

```yaml
- name: Run tunnel stability tests
  if: github.event_name == 'pull_request'
  env:
    HOME: ${{ github.workspace }}/.itests/home
    SSH_TEST_HOST: ssh://testuser@localhost:2222
    SSH_TEST_KEY_PATH: ${{ github.workspace }}/.itests/home/.ssh/id_ed25519
  run: |
    # Run only stability tests (long-running)
    go test -v -timeout=6m \
      -run 'TestManager_(LongRunning|MultiPort|UnderLoad)' \
      ./tests/integration/
```

### Test Execution Time
- **Test 1**: ~3.5 minutes
- **Test 2**: ~3.5 minutes  
- **Test 3**: ~3.5 minutes
- **Test 4**: ~3.5 minutes
- **Total**: ~14 minutes (can run in parallel if needed)

### Optimization Strategy
- Run in **parallel** where possible (Tests 1 & 2 can run together)
- Only run on **PR** (not every commit)
- Skip in **draft PRs** or use `[skip-stability]` tag
- **Nightly builds** can run full suite

---

## Expected Outcomes

### Before Fix (Current Behavior)
```
TestManager_LongRunningTunnelStability:
  ❌ SSH ControlMaster reconnected at 1m 23s
  ❌ HTTP requests failed after 1m 23s
  ❌ Only 16/36 requests succeeded (44% success rate)
  ❌ Detected "Permanently added..." in logs
```

### After Fix (Expected Behavior)
```
TestManager_LongRunningTunnelStability:
  ✅ SSH ControlMaster PID stable: 12345
  ✅ All 36 HTTP requests succeeded (100% success rate)
  ✅ Tunnel remained stable for 3m 2s
  ✅ No reconnection warnings in logs
```

---

## Implementation Priority

1. **Highest Priority**: Test Case 1 (Basic Stability)
   - Most direct validation of the fix
   - Easiest to implement with existing infrastructure
   
2. **High Priority**: Test Case 4 (Log Validation)
   - Complements Test 1 with diagnostic evidence
   - Catches subtle issues PID checking might miss

3. **Medium Priority**: Test Case 2 (Multi-Port)
   - Validates fix works with multiple tunnels
   - Real-world scenario

4. **Lower Priority**: Test Case 3 (Under Load)
   - Stress testing
   - Nice-to-have for confidence

---

## Questions for Feedback

1. **Test Duration**: Is 3 minutes sufficient, or should we test longer (e.g., 5 or 10 minutes)?

2. **Load Pattern**: For Test 3, is 10 req/sec appropriate, or should it be higher/lower?

3. **Parallel Execution**: Should these tests run in parallel to save CI time, or serially for clearer logs?

4. **Failure Threshold**: For "Under Load" test, is 98% success rate the right threshold, or should it be 99%?

5. **PID Detection Method**: Should we use `lsof` (requires elevated permissions) or `ssh -O check` parsing?

6. **CI Strategy**: Run on every PR, only manual trigger, or nightly?

7. **Log Capture**: Should we capture stderr separately to detect "Permanently added..." warnings more reliably?

---

## File Locations

### New Test File
`tests/integration/tunnel_stability_test.go`

### Helper Functions
Add to `tests/integration/helpers.go` (new file):
- `getSSHMasterPID()`
- `monitorSSHReconnections()`
- `captureManagerLogs()`

### Documentation
- This design doc: `docs/tunnel-stability-test-design.md`
- Test results template: `docs/tunnel-stability-test-results.md`