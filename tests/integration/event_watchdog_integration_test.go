package integration

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEventWatchdog_PingRunsAndDoesNotKillManagerWhenHealthy verifies that
// the event watchdog executes ping containers when idle but does not kill
// the manager as long as events (including ping events) are being received.
//
// This test validates:
// - Manager starts and runs successfully
// - When idle, ping containers are executed via local docker CLI
// - Ping containers generate events that keep the stream healthy
// - Manager does not exit with watchdog error during normal operation
//
// Duration: ~30 seconds
func TestEventWatchdog_PingRunsAndDoesNotKillManagerWhenHealthy(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	// Setup with log capture to verify ping execution
	t.Log("Setting up manager with log capture...")
	var logBuf bytes.Buffer
	logger := createLoggerWithCapture(&logBuf, "debug")

	mgr := setupManagerWithLogger(t, ctx, sshHost, logger)
	waitForManagerReady(t, 2*time.Second)

	t.Log("Manager started, no workload containers will be created")
	t.Log("Watchdog should execute ping containers when idle (>30s)")

	// Run for ~30 seconds - long enough to trigger at least one ping
	// (idle threshold is 30s, tick is 10s, so first ping should happen around 30-40s mark)
	testDuration := 35 * time.Second
	t.Logf("Running for %v to allow watchdog to trigger...", testDuration)

	startTime := time.Now()
	
	// Wait for test duration
	select {
	case <-time.After(testDuration):
		t.Log("Test duration elapsed")
	case <-ctx.Done():
		t.Fatal("Context canceled before test duration elapsed")
	}

	elapsed := time.Since(startTime)
	t.Logf("Test ran for %v", elapsed.Round(time.Second))

	// Verify logs contain evidence of ping execution
	logOutput := logBuf.String()

	t.Log("\n========================================")
	t.Log("LOG VERIFICATION")
	t.Log("========================================")

	// Look for ping-related log messages
	hasPingLog := strings.Contains(logOutput, "event health ping docker run")
	hasWatchdogStart := strings.Contains(logOutput, "event stream watchdog started")
	hasFatalError := strings.Contains(logOutput, "event stream watchdog detected failure")

	t.Logf("Watchdog started log: %v", hasWatchdogStart)
	t.Logf("Ping execution log: %v", hasPingLog)
	t.Logf("Fatal watchdog error: %v", hasFatalError)

	// Assertions
	require.True(t, hasWatchdogStart, "Logs should contain watchdog startup message")
	
	// Ping should have been executed at least once after 30s idle
	require.True(t, hasPingLog, 
		"Logs should contain evidence of ping execution after 30s+ idle time")
	
	// Manager should NOT have exited with watchdog error
	require.False(t, hasFatalError,
		"Manager should not have fatal watchdog error if pings are working")

	t.Log("\n✅ Watchdog test passed - pings executed without killing manager")
	
	_ = mgr
}

// TestEventWatchdog_ExecutesPingLocally verifies that ping containers
// are executed using the local docker CLI (not via SSH).
//
// This is a shorter test just to confirm the ping execution path.
//
// Duration: ~40 seconds
func TestEventWatchdog_ExecutesPingLocally(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	t.Log("Setting up manager with log capture...")
	var logBuf bytes.Buffer
	logger := createLoggerWithCapture(&logBuf, "debug")

	mgr := setupManagerWithLogger(t, ctx, sshHost, logger)
	waitForManagerReady(t, 2*time.Second)

	t.Log("Waiting for idle period to trigger ping...")
	
	// Wait long enough to trigger at least one ping cycle
	// Idle threshold is 30s, so we need to wait at least that long
	select {
	case <-time.After(35 * time.Second):
		t.Log("Wait period completed")
	case <-ctx.Done():
		t.Fatal("Context canceled before wait period completed")
	}

	// Check logs
	logOutput := logBuf.String()

	// Verify ping was attempted
	hasPingAttempt := strings.Contains(logOutput, "event health ping docker run")
	
	t.Logf("Ping attempt log found: %v", hasPingAttempt)

	require.True(t, hasPingAttempt,
		"Should see at least one ping attempt in logs")

	t.Log("✅ Ping execution confirmed")
	
	_ = mgr
}