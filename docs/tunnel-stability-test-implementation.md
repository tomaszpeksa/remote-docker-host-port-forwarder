# Tunnel Stability Test - Detailed Implementation Plan

## Overview

Implementation of 2 comprehensive tunnel stability tests using **Go's standard library only** (no external dependencies). Each test includes integrated log verification.

---

## Architecture Decision: Standard Library vs External Tools

### Why Standard Library (`net/http` + goroutines)?

✅ **Pros**:
- No external dependencies (keeps test suite simple)
- Full control over timing and concurrency
- Easy to integrate with existing test infrastructure
- Sufficient for our needs (we're testing tunnel stability, not load tool performance)
- Already imported in existing tests

❌ **External tools (vegeta, bombardier) cons**:
- Additional dependencies to maintain
- Overkill for integration tests
- Harder to integrate with Go test framework
- We don't need distributed load generation

### Load Generation Pattern

We'll use a **worker pool pattern** with goroutines:

```go
// Worker pool pattern for concurrent requests
func generateLoad(ctx context.Context, workerCount int, requestInterval time.Duration, targetFunc func() error) *LoadStats {
    var wg sync.WaitGroup
    stats := &LoadStats{/* ... */}
    
    for i := 0; i < workerCount; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            for {
                select {
                case <-ctx.Done():
                    return
                default:
                    // Execute request
                    // Record stats
                    time.Sleep(requestInterval)
                }
            }
        }(i)
    }
    
    wg.Wait()
    return stats
}
```

---

## Test Suite Structure

```
tests/integration/
├── tunnel_stability_test.go       (2 new test functions)
├── tunnel_stability_helpers.go    (new file with shared helpers)
└── end_to_end_test.go            (existing, reuse helpers)
```

---

## Test 1: TestManager_LongRunningTunnelStability

### Purpose
Verify SSH tunnel stability for 3+ minutes with multiple ports, continuous traffic, and no reconnections.

### Test Configuration

```go
const (
    testDuration     = 3 * time.Minute
    requestInterval  = 5 * time.Second
    numPorts        = 3
    startPort       = 19081
)
```

### Complete Implementation

```go
package integration

import (
    "bytes"
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "os"
    "regexp"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
    // ... other imports from existing tests
)

// TestManager_LongRunningTunnelStability verifies SSH tunnel stability
// over 3 minutes with multiple ports and integrated log verification.
//
// Test validates:
// - Multiple tunnels (3 ports) remain stable for 3 minutes
// - HTTP requests succeed continuously (>95% success rate)
// - SSH ControlMaster PID remains constant (no reconnections)
// - No reconnection warnings in logs
//
// Duration: ~3.5 minutes
func TestManager_LongRunningTunnelStability(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping long-running stability test in short mode")
    }

    sshHost := getTestSSHHost(t)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    // ===== STEP 1: Setup with Log Capture =====
    t.Log("Step 1: Setting up manager with log capture...")
    
    var logBuf bytes.Buffer
    logger := createLoggerWithCapture(&logBuf, "debug")
    
    // Create manager with captured logger (custom setup function)
    mgr := setupManagerWithLogger(t, ctx, sshHost, logger)
    waitForManagerReady(t, 2*time.Second)

    // ===== STEP 2: Start Multiple Containers =====
    t.Log("Step 2: Starting 3 nginx containers on different ports...")
    
    ports := []int{19081, 19082, 19083}
    containers := make([]string, 0, len(ports))
    cleanups := make([]func(), 0, len(ports))
    
    for _, port := range ports {
        containerID, cleanup := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{
            port: 80,
        })
        containers = append(containers, containerID)
        cleanups = append(cleanups, cleanup)
        
        t.Logf("  Container %s started on port %d", containerID[:12], port)
    }
    
    // Ensure cleanup runs
    defer func() {
        for _, cleanup := range cleanups {
            cleanup()
        }
    }()

    // ===== STEP 3: Wait for All Tunnels =====
    t.Log("Step 3: Waiting for all tunnels to establish...")
    
    for _, port := range ports {
        portOpened := waitForPortOpen(port, 5*time.Second)
        require.True(t, portOpened, "Port %d should open within 5s", port)
        t.Logf("  ✓ Port %d tunnel established", port)
    }

    // ===== STEP 4: Get Initial SSH Master PID =====
    t.Log("Step 4: Recording initial SSH ControlMaster PID...")
    
    initialPID := getSSHMasterPID(t, sshHost)
    require.NotEqual(t, -1, initialPID, "Should detect SSH ControlMaster PID")
    t.Logf("  Initial SSH ControlMaster PID: %d", initialPID)

    // ===== STEP 5: Continuous Testing for 3 Minutes =====
    t.Log("Step 5: Testing tunnel stability for 3 minutes...")
    
    testStart := time.Now()
    ticker := time.NewTicker(requestInterval)
    defer ticker.Stop()
    
    // Stats tracking
    stats := &TunnelStabilityStats{
        PerPortStats: make(map[int]*PortStats),
    }
    
    for _, port := range ports {
        stats.PerPortStats[port] = &PortStats{}
    }
    
    portIndex := 0
    reconnections := []ReconnectionEvent{}
    
    testLoop:
    for {
        select {
        case <-ctx.Done():
            break testLoop
            
        case <-ticker.C:
            elapsed := time.Since(testStart)
            
            if elapsed >= testDuration {
                break testLoop
            }
            
            // Test current port
            port := ports[portIndex]
            requestStart := time.Now()
            
            statusCode, _, err := testHTTPConnection(port)
            latency := time.Since(requestStart)
            
            portStats := stats.PerPortStats[port]
            portStats.TotalRequests++
            
            if err == nil && statusCode == 200 {
                portStats.SuccessCount++
                portStats.TotalLatency += latency
                
                if portStats.SuccessCount%5 == 0 {
                    t.Logf("  [%v] Port %d: %d/%d requests successful", 
                        elapsed.Round(time.Second), port, 
                        portStats.SuccessCount, portStats.TotalRequests)
                }
            } else {
                portStats.FailureCount++
                t.Logf("  ❌ [%v] Port %d request failed: %v", 
                    elapsed.Round(time.Second), port, err)
            }
            
            // Check SSH ControlMaster PID
            currentPID := getSSHMasterPID(t, sshHost)
            if currentPID != initialPID && currentPID != -1 {
                reconnection := ReconnectionEvent{
                    Time:        time.Now(),
                    OldPID:      initialPID,
                    NewPID:      currentPID,
                    ElapsedTime: elapsed,
                }
                reconnections = append(reconnections, reconnection)
                
                t.Errorf("❌ SSH ControlMaster reconnected! "+
                    "Old PID: %d, New PID: %d, Time: %v", 
                    initialPID, currentPID, elapsed.Round(time.Second))
                
                initialPID = currentPID // Update for next check
            }
            
            // Rotate to next port
            portIndex = (portIndex + 1) % len(ports)
        }
    }
    
    totalDuration := time.Since(testStart)

    // ===== STEP 6: Calculate and Display Results =====
    t.Log("\n========================================")
    t.Log("TUNNEL STABILITY TEST RESULTS")
    t.Log("========================================")
    
    stats.TotalDuration = totalDuration
    stats.ExpectedRequests = int(testDuration / requestInterval)
    
    for port, portStats := range stats.PerPortStats {
        successRate := float64(portStats.SuccessCount) / float64(portStats.TotalRequests) * 100
        avgLatency := time.Duration(0)
        if portStats.SuccessCount > 0 {
            avgLatency = portStats.TotalLatency / time.Duration(portStats.SuccessCount)
        }
        
        t.Logf("\nPort %d:", port)
        t.Logf("  Total Requests:  %d", portStats.TotalRequests)
        t.Logf("  Successful:      %d", portStats.SuccessCount)
        t.Logf("  Failed:          %d", portStats.FailureCount)
        t.Logf("  Success Rate:    %.1f%%", successRate)
        t.Logf("  Avg Latency:     %v", avgLatency.Round(time.Millisecond))
        
        stats.TotalSuccesses += portStats.SuccessCount
        stats.TotalFailures += portStats.FailureCount
    }
    
    overallSuccessRate := float64(stats.TotalSuccesses) / 
        float64(stats.TotalSuccesses+stats.TotalFailures) * 100
    
    t.Logf("\nOverall:")
    t.Logf("  Duration:        %v", totalDuration.Round(time.Second))
    t.Logf("  Success Rate:    %.1f%%", overallSuccessRate)
    t.Logf("  Reconnections:   %d", len(reconnections))

    // ===== STEP 7: Verify Logs (NO RECONNECTION WARNINGS) =====
    t.Log("\n========================================")
    t.Log("LOG VERIFICATION")
    t.Log("========================================")
    
    logOutput := logBuf.String()
    logIssues := verifyNoReconnectionWarnings(t, logOutput)
    
    if len(logIssues) > 0 {
        t.Log("\n❌ Found reconnection indicators in logs:")
        for _, issue := range logIssues {
            t.Logf("  - %s", issue)
        }
    } else {
        t.Log("\n✅ No reconnection warnings found in logs")
    }

    // ===== STEP 8: Assertions =====
    t.Log("\n========================================")
    t.Log("ASSERTIONS")
    t.Log("========================================")
    
    // Assert no reconnections
    assert.Empty(t, reconnections, 
        "SSH ControlMaster should not reconnect during test")
    
    // Assert high success rate (>95%)
    assert.GreaterOrEqual(t, overallSuccessRate, 95.0, 
        "Overall success rate should be >95%%")
    
    // Assert per-port success rate
    for port, portStats := range stats.PerPortStats {
        portSuccessRate := float64(portStats.SuccessCount) / 
            float64(portStats.TotalRequests) * 100
        assert.GreaterOrEqual(t, portSuccessRate, 95.0,
            "Port %d success rate should be >95%%", port)
    }
    
    // Assert no log issues
    assert.Empty(t, logIssues, 
        "Logs should not contain reconnection warnings")
    
    // Assert minimum request count
    minExpectedRequests := int(testDuration/requestInterval) * len(ports) * 90 / 100 // 90% of expected
    totalRequests := stats.TotalSuccesses + stats.TotalFailures
    assert.GreaterOrEqual(t, totalRequests, minExpectedRequests,
        "Should complete at least 90%% of expected requests")
    
    if len(reconnections) == 0 && overallSuccessRate >= 95.0 && len(logIssues) == 0 {
        t.Log("\n✅ ALL CHECKS PASSED - Tunnel stability verified!")
    } else {
        t.Log("\n❌ STABILITY TEST FAILED - See errors above")
    }
}
```

### Supporting Structures

```go
// TunnelStabilityStats tracks tunnel test statistics
type TunnelStabilityStats struct {
    TotalDuration     time.Duration
    ExpectedRequests  int
    TotalSuccesses    int
    TotalFailures     int
    PerPortStats      map[int]*PortStats
}

// PortStats tracks per-port statistics
type PortStats struct {
    TotalRequests int
    SuccessCount  int
    FailureCount  int
    TotalLatency  time.Duration
}

// ReconnectionEvent records when SSH master reconnects
type ReconnectionEvent struct {
    Time        time.Time
    OldPID      int
    NewPID      int
    ElapsedTime time.Duration
}
```

---

## Test 2: TestManager_TunnelStabilityUnderLoad

### Purpose
Stress test tunnel stability with high-frequency concurrent requests.

### Test Configuration

```go
const (
    loadTestDuration  = 3 * time.Minute
    workerCount       = 4                    // 4 concurrent workers
    requestsPerSecond = 10                   // Per worker = 40 req/s total
    requestInterval   = time.Second / requestsPerSecond
    targetPort        = 19090
)
```

### Complete Implementation

```go
// TestManager_TunnelStabilityUnderLoad verifies SSH tunnel stability
// under sustained high-frequency load.
//
// Test validates:
// - Tunnel handles 40 requests/second (4 workers × 10 req/s each)
// - Success rate >98% under load
// - SSH ControlMaster remains stable
// - No reconnection warnings in logs
// - Latency remains reasonable (<500ms p95)
//
// Duration: ~3.5 minutes
func TestManager_TunnelStabilityUnderLoad(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping load test in short mode")
    }

    sshHost := getTestSSHHost(t)

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
    defer cancel()

    // ===== STEP 1: Setup with Log Capture =====
    t.Log("Step 1: Setting up manager with log capture...")
    
    var logBuf bytes.Buffer
    logger := createLoggerWithCapture(&logBuf, "debug")
    
    mgr := setupManagerWithLogger(t, ctx, sshHost, logger)
    waitForManagerReady(t, 2*time.Second)

    // ===== STEP 2: Start Container =====
    t.Log("Step 2: Starting nginx container...")
    
    containerID, cleanup := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{
        targetPort: 80,
    })
    defer cleanup()
    
    t.Logf("  Container %s started on port %d", containerID[:12], targetPort)

    // ===== STEP 3: Wait for Tunnel =====
    t.Log("Step 3: Waiting for tunnel to establish...")
    
    portOpened := waitForPortOpen(targetPort, 5*time.Second)
    require.True(t, portOpened, "Port should open within 5s")
    t.Logf("  ✓ Tunnel established on port %d", targetPort)

    // ===== STEP 4: Get Initial SSH Master PID =====
    t.Log("Step 4: Recording initial SSH ControlMaster PID...")
    
    initialPID := getSSHMasterPID(t, sshHost)
    require.NotEqual(t, -1, initialPID, "Should detect SSH ControlMaster PID")
    t.Logf("  Initial SSH ControlMaster PID: %d", initialPID)

    // ===== STEP 5: Generate Load =====
    t.Log("Step 5: Starting load generation...")
    t.Logf("  Workers: %d", workerCount)
    t.Logf("  Requests per second: %d per worker (%d total)", 
        requestsPerSecond, workerCount*requestsPerSecond)
    t.Logf("  Duration: %v", loadTestDuration)
    
    loadStats := generateLoad(ctx, workerCount, requestInterval, targetPort, loadTestDuration)

    // ===== STEP 6: Monitor SSH PID During Load =====
    pidChanges := monitorSSHPIDChanges(t, sshHost, initialPID, loadTestDuration)

    // ===== STEP 7: Display Results =====
    t.Log("\n========================================")
    t.Log("LOAD TEST RESULTS")
    t.Log("========================================")
    
    successRate := float64(loadStats.SuccessCount) / float64(loadStats.TotalRequests) * 100
    
    t.Logf("\nLoad Statistics:")
    t.Logf("  Duration:          %v", loadStats.Duration.Round(time.Second))
    t.Logf("  Total Requests:    %d", loadStats.TotalRequests)
    t.Logf("  Successful:        %d", loadStats.SuccessCount)
    t.Logf("  Failed:            %d", loadStats.FailureCount)
    t.Logf("  Success Rate:      %.2f%%", successRate)
    t.Logf("  Requests/Second:   %.1f", loadStats.RequestsPerSecond)
    
    t.Logf("\nLatency Statistics:")
    t.Logf("  Min:     %v", loadStats.MinLatency.Round(time.Millisecond))
    t.Logf("  Max:     %v", loadStats.MaxLatency.Round(time.Millisecond))
    t.Logf("  Avg:     %v", loadStats.AvgLatency.Round(time.Millisecond))
    t.Logf("  Median:  %v", loadStats.MedianLatency.Round(time.Millisecond))
    t.Logf("  P95:     %v", loadStats.P95Latency.Round(time.Millisecond))
    t.Logf("  P99:     %v", loadStats.P99Latency.Round(time.Millisecond))
    
    t.Logf("\nSSH Stability:")
    t.Logf("  PID Changes:  %d", len(pidChanges))
    
    if len(pidChanges) > 0 {
        t.Log("\n❌ SSH ControlMaster reconnections detected:")
        for i, change := range pidChanges {
            t.Logf("  %d. At %v: PID %d → %d", 
                i+1, change.Time.Round(time.Second), change.OldPID, change.NewPID)
        }
    }

    // ===== STEP 8: Verify Logs =====
    t.Log("\n========================================")
    t.Log("LOG VERIFICATION")
    t.Log("========================================")
    
    logOutput := logBuf.String()
    logIssues := verifyNoReconnectionWarnings(t, logOutput)
    
    if len(logIssues) > 0 {
        t.Log("\n❌ Found reconnection indicators in logs:")
        for _, issue := range logIssues {
            t.Logf("  - %s", issue)
        }
    } else {
        t.Log("\n✅ No reconnection warnings found in logs")
    }

    // ===== STEP 9: Assertions =====
    t.Log("\n========================================")
    t.Log("ASSERTIONS")
    t.Log("========================================")
    
    // Assert high success rate under load (>98%)
    assert.GreaterOrEqual(t, successRate, 98.0,
        "Success rate should be >98%% under load")
    
    // Assert no SSH reconnections
    assert.Empty(t, pidChanges,
        "SSH ControlMaster should not reconnect under load")
    
    // Assert reasonable latency (P95 < 500ms)
    assert.Less(t, loadStats.P95Latency, 500*time.Millisecond,
        "P95 latency should be <500ms")
    
    // Assert no log issues
    assert.Empty(t, logIssues,
        "Logs should not contain reconnection warnings")
    
    // Assert minimum request count (expect ~7200 requests: 4 workers × 10 req/s × 180s)
    expectedRequests := workerCount * requestsPerSecond * int(loadTestDuration.Seconds())
    minRequests := expectedRequests * 90 / 100 // 90% of expected
    assert.GreaterOrEqual(t, loadStats.TotalRequests, minRequests,
        "Should complete at least 90%% of expected requests")
    
    if len(pidChanges) == 0 && successRate >= 98.0 && len(logIssues) == 0 {
        t.Log("\n✅ ALL CHECKS PASSED - Tunnel stable under load!")
    } else {
        t.Log("\n❌ LOAD TEST FAILED - See errors above")
    }
    
    _ = mgr
}
```

### Load Generation Function

```go
// LoadStats tracks load test statistics
type LoadStats struct {
    Duration          time.Duration
    TotalRequests     int
    SuccessCount      int
    FailureCount      int
    RequestsPerSecond float64
    
    // Latency metrics
    MinLatency    time.Duration
    MaxLatency    time.Duration
    AvgLatency    time.Duration
    MedianLatency time.Duration
    P95Latency    time.Duration
    P99Latency    time.Duration
    
    Latencies []time.Duration // For percentile calculation
}

// generateLoad creates concurrent workers making HTTP requests
func generateLoad(
    ctx context.Context,
    workerCount int,
    requestInterval time.Duration,
    targetPort int,
    duration time.Duration,
) *LoadStats {
    var (
        wg    sync.WaitGroup
        mu    sync.Mutex
        stats = &LoadStats{
            MinLatency: time.Hour, // Will be updated
            Latencies:  make([]time.Duration, 0, 10000),
        }
    )
    
    // Create HTTP client with timeouts
    client := &http.Client{
        Timeout: 3 * time.Second,
        Transport: &http.Transport{
            MaxIdleConnsPerHost: workerCount * 2,
            IdleConnTimeout:     90 * time.Second,
        },
    }
    
    startTime := time.Now()
    testCtx, cancel := context.WithTimeout(ctx, duration+10*time.Second)
    defer cancel()
    
    // Launch workers
    for i := 0; i < workerCount; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            
            ticker := time.NewTicker(requestInterval)
            defer ticker.Stop()
            
            for {
                select {
                case <-testCtx.Done():
                    return
                    
                case <-ticker.C:
                    if time.Since(startTime) >= duration {
                        return
                    }
                    
                    // Make HTTP request
                    reqStart := time.Now()
                    resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", targetPort))
                    latency := time.Since(reqStart)
                    
                    // Record stats
                    mu.Lock()
                    stats.TotalRequests++
                    
                    if err == nil && resp.StatusCode == 200 {
                        stats.SuccessCount++
                        resp.Body.Close()
                        
                        // Update latency stats
                        stats.Latencies = append(stats.Latencies, latency)
                        if latency < stats.MinLatency {
                            stats.MinLatency = latency
                        }
                        if latency > stats.MaxLatency {
                            stats.MaxLatency = latency
                        }
                    } else {
                        stats.FailureCount++
                        if resp != nil {
                            resp.Body.Close()
                        }
                    }
                    mu.Unlock()
                }
            }
        }(i)
    }
    
    wg.Wait()
    
    // Calculate final statistics
    stats.Duration = time.Since(startTime)
    stats.RequestsPerSecond = float64(stats.TotalRequests) / stats.Duration.Seconds()
    
    // Calculate latency percentiles
    if len(stats.Latencies) > 0 {
        sort.Slice(stats.Latencies, func(i, j int) bool {
            return stats.Latencies[i] < stats.Latencies[j]
        })
        
        var totalLatency time.Duration
        for _, l := range stats.Latencies {
            totalLatency += l
        }
        stats.AvgLatency = totalLatency / time.Duration(len(stats.Latencies))
        
        stats.MedianLatency = stats.Latencies[len(stats.Latencies)/2]
        stats.P95Latency = stats.Latencies[int(float64(len(stats.Latencies))*0.95)]
        stats.P99Latency = stats.Latencies[int(float64(len(stats.Latencies))*0.99)]
    }
    
    return stats
}
```

---

## Helper Functions (tunnel_stability_helpers.go)

```go
package integration

import (
    "bytes"
    "context"
    "fmt"
    "io"
    "log/slog"
    "os"
    "os/exec"
    "regexp"
    "strconv"
    "strings"
    "testing"
    "time"
)

// createLoggerWithCapture creates a logger that writes to both stdout and a buffer
func createLoggerWithCapture(buf *bytes.Buffer, level string) *slog.Logger {
    // Multi-writer: stdout + buffer
    multiWriter := io.MultiWriter(os.Stdout, buf)
    
    handler := slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
        Level: func() slog.Level {
            switch level {
            case "debug":
                return slog.LevelDebug
            case "info":
                return slog.LevelInfo
            default:
                return slog.LevelInfo
            }
        }(),
    })
    
    return slog.New(handler)
}

// setupManagerWithLogger is like setupManager but accepts a custom logger
func setupManagerWithLogger(
    t *testing.T,
    ctx context.Context,
    sshHost string,
    logger *slog.Logger,
) *manager.Manager {
    t.Helper()
    
    // Create SSH master
    master, err := ssh.NewMaster(sshHost, logger)
    require.NoError(t, err, "Should create SSH master")
    
    err = master.Open(ctx)
    require.NoError(t, err, "Should open SSH ControlMaster")
    
    t.Cleanup(func() {
        master.Close()
    })
    
    // Get control path
    controlPath, err := ssh.DeriveControlPath(sshHost)
    require.NoError(t, err, "Should derive control path")
    
    // Create state
    st := state.NewState()
    
    // Create reconciler
    reconciler := reconcile.NewReconciler(st, logger)
    
    // Create event reader
    eventReader := docker.NewEventReader(sshHost, controlPath, logger)
    
    // Create config
    cfg := &config.Config{
        Host: sshHost,
    }
    
    // Create manager
    mgr := manager.NewManager(cfg, eventReader, reconciler, master, st, logger)
    
    // Start manager in background
    go func() {
        if err := mgr.Run(ctx); err != nil && ctx.Err() == nil {
            t.Logf("Manager stopped with error: %v", err)
        }
    }()
    
    return mgr
}

// getSSHMasterPID returns the PID of the SSH ControlMaster process
// Returns -1 if not found
func getSSHMasterPID(t *testing.T, sshHost string) int {
    t.Helper()
    
    controlPath, err := ssh.DeriveControlPath(sshHost)
    if err != nil {
        t.Logf("Failed to derive control path: %v", err)
        return -1
    }
    
    sshHostClean, port, err := ssh.ParseHost(sshHost)
    if err != nil {
        return -1
    }
    
    // Use SSH -O check to get PID
    args := []string{"-S", controlPath, "-O", "check"}
    if port != "" {
        args = append(args, "-p", port)
    }
    args = append(args, sshHostClean)
    
    keyPath := os.Getenv("SSH_TEST_KEY_PATH")
    if keyPath != "" {
        args = append([]string{"-i", keyPath}, args...)
    }
    
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

// PIDChange records when SSH master PID changes
type PIDChange struct {
    Time   time.Time
    OldPID int
    NewPID int
}

// monitorSSHPIDChanges monitors SSH ControlMaster PID during a test
func monitorSSHPIDChanges(
    t *testing.T,
    sshHost string,
    initialPID int,
    duration time.Duration,
) []PIDChange {
    t.Helper()
    
    changes := make([]PIDChange, 0)
    currentPID := initialPID
    
    startTime := time.Now()
    ticker := time.NewTicker(2 * time.Second)
    defer ticker.Stop()
    
    for time.Since(startTime) < duration {
        <-ticker.C
        
        newPID := getSSHMasterPID(t, sshHost)
        if newPID != -1 && newPID != currentPID {
            changes = append(changes, PIDChange{
                Time:   time.Now(),
                OldPID: currentPID,
                NewPID: newPID,
            })
            currentPID = newPID
        }
    }
    
    return changes
}

// verifyNoReconnectionWarnings checks logs for reconnection indicators
// Returns list of issues found
func verifyNoReconnectionWarnings(t *testing.T, logOutput string) []string {
    t.Helper()
    
    issues := make([]string, 0)
    
    // Patterns that indicate reconnections
    patterns := map[string]string{
        "Permanently added":                      "New SSH connection (not using ControlMaster)",
        "event stream failed after":              "Event stream failures",
        "SSH ControlMaster is dead, recreating": "ControlMaster recreation",
        "consecutive_failures=[3-9]":             "Multiple consecutive failures (3+)",
        "consecutive_failures=[1-9][0-9]":        "High failure count (10+)",
    }
    
    for pattern, description := range patterns {
        re := regexp.MustCompile(pattern)
        matches := re.FindAllString(logOutput, -1)
        
        if len(matches) > 0 {
            issues = append(issues, fmt.Sprintf("%s: found %d instances", 
                description, len(matches)))
        }
    }
    
    // Check for single stream start (stability indicator)
    streamStarts := strings.Count(logOutput, "DIAGNOSTIC: Starting Docker events stream")
    if streamStarts > 1 {
        issues = append(issues, fmt.Sprintf(
            "Stream restarted %d times (expected 1)", streamStarts))
    }
    
    // Verify we DO have health confirmations (positive check)
    healthConfirms := strings.Count(logOutput, "SSH ControlMaster verified healthy")
    if healthConfirms == 0 {
        issues = append(issues, 
            "No health check confirmations found (expected multiple)")
    }
    
    return issues
}
```

---

## Test Execution

### Local Testing

```bash
# Run both stability tests
make itest-up
HOME=$(pwd)/.itests/home \
SSH_TEST_HOST=ssh://testuser@localhost:2222 \
SSH_TEST_KEY_PATH=$(pwd)/.itests/home/.ssh/id_ed25519 \
go test -v -timeout=10m \
  -run 'TestManager_(LongRunning|TunnelStability)' \
  ./tests/integration/

# Or run individually
go test -v -timeout=5m -run TestManager_LongRunningTunnelStability ./tests/integration/
go test -v -timeout=5m -run TestManager_TunnelStabilityUnderLoad ./tests/integration/
```

### CI Integration (GitHub Actions)

```yaml
- name: Run stability tests
  if: github.event_name == 'pull_request' && !contains(github.event.pull_request.title, '[skip-stability]')
  env:
    HOME: ${{ github.workspace }}/.itests/home
    SSH_TEST_HOST: ssh://testuser@localhost:2222
    SSH_TEST_KEY_PATH: ${{ github.workspace }}/.itests/home/.ssh/id_ed25519
  run: |
    go test -v -timeout=10m \
      -run 'TestManager_(LongRunning|TunnelStability)' \
      ./tests/integration/ | tee stability-test-results.log

- name: Upload test results
  if: always()
  uses: actions/upload-artifact@v3
  with:
    name: stability-test-results
    path: stability-test-results.log
```

---

## Expected Test Output

### Before Fix

```
=== RUN   TestManager_LongRunningTunnelStability
Step 1: Setting up manager with log capture...
Step 2: Starting 3 nginx containers on different ports...
  Container a1b2c3d4e5f6 started on port 19081
  Container f6e5d4c3b2a1 started on port 19082
  Container 1a2b3c4d5e6f started on port 19083
Step 3: Waiting for all tunnels to establish...
  ✓ Port 19081 tunnel established
  ✓ Port 19082 tunnel established
  ✓ Port 19083 tunnel established
Step 4: Recording initial SSH ControlMaster PID...
  Initial SSH ControlMaster PID: 12345
Step 5: Testing tunnel stability for 3 minutes...
  [1m] Port 19081: 5/5 requests successful
  [1m] Port 19082: 5/5 requests successful
  ❌ [1m23s] SSH ControlMaster reconnected! Old PID: 12345, New PID: 12567, Time: 1m23s
  ❌ [1m24s] Port 19083 request failed: connection refused
  ...

========================================
TUNNEL STABILITY TEST RESULTS
========================================
Port 19081:
  Total Requests:  12
  Successful:      10
  Failed:          2
  Success Rate:    83.3%
  ...

Overall:
  Duration:        3m 2s
  Success Rate:    78.5%
  Reconnections:   2

========================================
LOG VERIFICATION
========================================
❌ Found reconnection indicators in logs:
  - New SSH connection (not using ControlMaster): found 3 instances
  - Event stream failures: found 12 instances
  - Stream restarted 3 times (expected 1)

--- FAIL: TestManager_LongRunningTunnelStability (182.45s)
```

### After Fix

```
=== RUN   TestManager_LongRunningTunnelStability
Step 1: Setting up manager with log capture...
Step 2: Starting 3 nginx containers on different ports...
  Container a1b2c3d4e5f6 started on port 19081
  Container f6e5d4c3b2a1 started on port 19082
  Container 1a2b3c4d5e6f started on port 19083
Step 3: Waiting for all tunnels to establish...
  ✓ Port 19081 tunnel established
  ✓ Port 19082 tunnel established
  ✓ Port 19083 tunnel established
Step 4: Recording initial SSH ControlMaster PID...
  Initial SSH ControlMaster PID: 12345
Step 5: Testing tunnel stability for 3 minutes...
  [1m] Port 19081: 5/5 requests successful
  [1m] Port 19082: 5/5 requests successful
  [1m] Port 19083: 5/5 requests successful
  [2m] Port 19081: 10/10 requests successful
  [2m] Port 19082: 10/10 requests successful
  [2m] Port 19083: 10/10 requests successful
  [3m] Port 19081: 15/15 requests successful
  [3m] Port 19082: 15/15 requests successful
  [3m] Port 19083: 15/15 requests successful

========================================
TUNNEL STABILITY TEST RESULTS
========================================
Port 19081:
  Total Requests:  36
  Successful:      36
  Failed:          0
  Success Rate:    100.0%
  Avg Latency:     45ms

Port 19082:
  Total Requests:  36
  Successful:      36
  Failed:          0
  Success Rate:    100.0%
  Avg Latency:     43ms

Port 19083:
  Total Requests:  36
  Successful:      36
  Failed:          0
  Success Rate:    100.0%
  Avg Latency:     47ms

Overall:
  Duration:        3m 1s
  Success Rate:    100.0%
  Reconnections:   0

========================================
LOG VERIFICATION
========================================
✅ No reconnection warnings found in logs

========================================
ASSERTIONS
========================================
✅ ALL CHECKS PASSED - Tunnel stability verified!
--- PASS: TestManager_LongRunningTunnelStability (181.23s)
```

---

## Summary

### Implementation Approach

1. **Standard Library Only**: Using `net/http` + goroutines (no external dependencies)
2. **Integrated Log Verification**: Every test captures and verifies logs
3. **2 Comprehensive Tests**: 
   - Long-running multi-port (basic stability)
   - High-load single port (stress test)
4. **Clear Pass/Fail**: Detailed output with specific assertions

### Why This Design Works

✅ **Reuses existing infrastructure** (SSH container, helpers)  
✅ **No new dependencies** (standard library sufficient)  
✅ **Clear validation** (PID tracking + log verification)  
✅ **Realistic scenarios** (multi-port + load testing)  
✅ **CI-friendly** (~7 minutes total if run in parallel)

### Next Steps

1. Review this implementation plan
2. Provide feedback on:
   - Load parameters (4 workers × 10 req/s = 40 req/s total)
   - Success rate thresholds (95% for stability, 98% for load)
   - Any modifications needed
3. I'll implement the actual test files with this detailed design