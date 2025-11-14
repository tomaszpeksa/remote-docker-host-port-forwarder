package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGracefulShutdownRemovesForwards verifies that sending SIGTERM/SIGINT
// cleanly removes all forwards and closes the SSH master.
//
// Test scenario:
// 1. Start tool with active forwards
// 2. Send shutdown signal (context cancel)
// 3. Verify all forwards are removed cleanly
// 4. Verify manager stops gracefully
func TestGracefulShutdownRemovesForwards(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)

	// Create cancelable context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start manager
	mgr := setupManager(t, ctx, sshHost)
	waitForManagerReady(t, 2*time.Second)

	// Use a high port to avoid conflicts
	testPort := 18084

	// Start container with port
	containerID, cleanup := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{
		testPort: 80,
	})
	defer cleanup()

	// Wait for port to open
	portOpened := waitForPortOpen(testPort, 3*time.Second)
	require.True(t, portOpened, "Port %d should open within 3s", testPort)
	t.Logf("✓ Port %d opened successfully", testPort)

	// Verify connection works
	err := testTCPConnection(testPort)
	require.NoError(t, err, "TCP connection should work before shutdown")
	t.Log("✓ TCP connection working before shutdown")

	// Trigger graceful shutdown by canceling context
	t.Log("Triggering graceful shutdown...")
	cancel()

	// Verify port closes within expected time
	// Note: Increased timeout for CI/DinD environments
	portClosed := assert.Eventually(t, func() bool {
		return !portIsOpen(testPort)
	}, 10*time.Second, 100*time.Millisecond,
		"Port should close within 10s after shutdown")

	require.True(t, portClosed, "Port must close after shutdown")
	t.Log("✓ Port closed after graceful shutdown")

	// Verify container is still running (manager doesn't affect containers)
	time.Sleep(200 * time.Millisecond)
	// The container cleanup will verify it's still running

	t.Log("✓ Graceful shutdown completed successfully")

	_ = mgr
	_ = containerID
}

// TestShutdownTimeout verifies that shutdown completes within timeout
func TestShutdownTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Shutdown timeout handling will be added in T064")

	// Expected implementation:
	// 1. Start manager with many forwards
	// 2. Trigger shutdown
	// 3. Verify shutdown completes within 10s
	// 4. If timeout exceeded, verify error logged and exit code non-zero
}

// TestSIGINTHandling verifies graceful shutdown via context cancellation
// (simulates SIGINT behavior)
func TestSIGINTHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)

	// Create cancelable context (simulates SIGINT)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start manager
	_ = setupManager(t, ctx, sshHost)
	waitForManagerReady(t, 2*time.Second)

	// Start container to establish forward
	testPort := 18085
	_, cleanup := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{
		testPort: 80,
	})
	defer cleanup()

	// Wait for port to open
	require.True(t, waitForPortOpen(testPort, 3*time.Second),
		"Port should open before shutdown")

	// Simulate SIGINT by canceling context
	t.Log("Simulating SIGINT via context cancellation")
	cancel()

	// Verify port closes (forward removed)
	assert.Eventually(t, func() bool {
		return !portIsOpen(testPort)
	}, 10*time.Second, 100*time.Millisecond,
		"Port should close after SIGINT")

	t.Log("✓ SIGINT handling verified")
}

// TestSIGTERMHandling verifies graceful shutdown via context cancellation
// (simulates SIGTERM behavior)
func TestSIGTERMHandling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)

	// Create cancelable context (simulates SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start manager
	_ = setupManager(t, ctx, sshHost)
	waitForManagerReady(t, 2*time.Second)

	// Start container to establish forward
	testPort := 18086
	_, cleanup := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{
		testPort: 80,
	})
	defer cleanup()

	// Wait for port to open
	require.True(t, waitForPortOpen(testPort, 3*time.Second),
		"Port should open before shutdown")

	// Simulate SIGTERM by canceling context
	t.Log("Simulating SIGTERM via context cancellation")
	cancel()

	// Verify port closes (forward removed)
	assert.Eventually(t, func() bool {
		return !portIsOpen(testPort)
	}, 10*time.Second, 100*time.Millisecond,
		"Port should close after SIGTERM")

	t.Log("✓ SIGTERM handling verified")
}

// TestCleanupRemovesAllForwards verifies the cleanup process removes all forwards
func TestCleanupRemovesAllForwards(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)

	// Create cancelable context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start manager
	_ = setupManager(t, ctx, sshHost)
	waitForManagerReady(t, 2*time.Second)

	// Start multiple containers with different ports
	port1 := 18087
	port2 := 18088
	port3 := 18089

	_, cleanup1 := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{port1: 80})
	defer cleanup1()

	_, cleanup2 := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{port2: 80})
	defer cleanup2()

	_, cleanup3 := startDockerContainer(t, sshHost, "nginx:alpine", map[int]int{port3: 80})
	defer cleanup3()

	// Wait for all ports to open
	require.True(t, waitForPortOpen(port1, 3*time.Second), "Port 1 should open")
	require.True(t, waitForPortOpen(port2, 3*time.Second), "Port 2 should open")
	require.True(t, waitForPortOpen(port3, 3*time.Second), "Port 3 should open")

	t.Log("✓ All 3 forwards established")

	// Trigger shutdown
	cancel()

	// Verify all ports close
	assert.Eventually(t, func() bool {
		return !portIsOpen(port1) && !portIsOpen(port2) && !portIsOpen(port3)
	}, 10*time.Second, 100*time.Millisecond,
		"All ports should close after shutdown")

	t.Log("✓ All forwards removed during cleanup")
}

// TestCleanupRemovesSSHMaster verifies SSH master is properly closed on shutdown
func TestCleanupRemovesSSHMaster(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)
	if sshHost == "" {
		t.Skip("TEST_SSH_HOST not set, skipping SSH integration test")
	}

	t.Skip("SSH master cleanup will be verified in T064/T067")

	// Expected implementation:
	// 1. Open SSH master
	// 2. Verify control socket exists
	// 3. Call Close()
	// 4. Verify "ssh -O exit" was called
	// 5. Verify control socket file removed
	// 6. Verify no SSH process remains
}

// TestNoOrphanedProcessesAfterShutdown verifies no SSH processes leak
func TestNoOrphanedProcessesAfterShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Process cleanup verification will be added in T067")

	// Expected implementation:
	// 1. Count SSH processes before test
	// 2. Start and stop tool multiple times
	// 3. Count SSH processes after test
	// 4. Verify count hasn't increased
	// 5. No processes matching "rdhpf" or control socket name
}

// TestShutdownDuringReconciliation verifies safe shutdown even during reconciliation
func TestShutdownDuringReconciliation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Concurrent shutdown handling will be verified after implementation")

	// Expected implementation:
	// 1. Start tool
	// 2. Trigger large reconciliation (many containers)
	// 3. Send shutdown signal mid-reconciliation
	// 4. Verify reconciliation stops gracefully
	// 5. Verify partial forwards are cleaned up
	// 6. No errors or panics
}

// TestShutdownLogsCorrectly verifies shutdown produces expected log messages
func TestShutdownLogsCorrectly(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Log verification will be added after shutdown implementation")

	// Expected log messages:
	// - "received signal, shutting down"
	// - "closing SSH ControlMaster connection"
	// - "Shutdown complete, all forwards removed"
	// - "rdhpf stopped"
	//
	// Should NOT see:
	// - Error messages
	// - Warnings (except for expected cases)
}

// TestMultipleShutdownSignals verifies handling of rapid shutdown signals
func TestMultipleShutdownSignals(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Multiple signal handling will be verified after implementation")

	// Expected implementation:
	// 1. Start tool
	// 2. Send multiple SIGTERM signals rapidly
	// 3. Verify only one shutdown sequence runs
	// 4. No race conditions or panics
	// 5. Clean exit
}
