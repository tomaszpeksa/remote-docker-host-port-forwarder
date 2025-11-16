package integration

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	// Create manager with captured logger
	mgr := setupManagerWithLogger(t, ctx, sshHost, logger)
	waitForManagerReady(t, 2*time.Second)

	// ===== STEP 2: Start Multiple Containers =====
	t.Log("Step 2: Starting 3 nginx containers on different ports...")

	ports := []int{19081, 19082, 19083}
	cleanups := make([]func(), 0, len(ports))

	for _, port := range ports {
		containerID, cleanup := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{
			port: 80,
		})
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

	const (
		testDuration    = 3 * time.Minute
		requestInterval = 5 * time.Second
	)

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

			// Test ALL ports on each tick for comprehensive stability testing
			for _, port := range ports {
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
			}

			// Check SSH ControlMaster PID once per tick (not per port)
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

	_ = mgr
}

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

	const targetPort = 19090
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

	const (
		workerCount       = 4
		requestsPerSecond = 10
		loadTestDuration  = 3 * time.Minute
		requestInterval   = time.Second / requestsPerSecond
	)

	t.Logf("  Workers: %d", workerCount)
	t.Logf("  Requests per second: %d per worker (%d total)",
		requestsPerSecond, workerCount*requestsPerSecond)
	t.Logf("  Duration: %v", loadTestDuration)

	// Launch load generation in background
	loadDone := make(chan *LoadStats)
	go func() {
		stats := generateLoad(ctx, workerCount, requestInterval, targetPort, loadTestDuration)
		loadDone <- stats
	}()

	// Monitor SSH PID during load
	pidChanges := monitorSSHPIDChanges(t, sshHost, initialPID, loadTestDuration)

	// Wait for load generation to complete
	loadStats := <-loadDone

	// ===== STEP 6: Display Results =====
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

	// ===== STEP 7: Verify Logs =====
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
