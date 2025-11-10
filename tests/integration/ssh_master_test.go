package integration

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// getTestSSHHost returns the SSH host to use for testing, or skips the test
// if SSH_TEST_HOST environment variable is not set.
func getTestSSHHost(t *testing.T) string {
	host := os.Getenv("SSH_TEST_HOST")
	if host == "" {
		t.Skip("Skipping integration test: SSH_TEST_HOST not set. " +
			"Set it to ssh://user@host to enable SSH integration tests.")
	}
	return host
}

// TestSSHMaster_OpenAndClose tests the basic lifecycle of SSH ControlMaster
func TestSSHMaster_OpenAndClose(t *testing.T) {
	host := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(host, logger)
	require.NoError(t, err, "Should create master successfully")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test Open
	err = master.Open(ctx)
	require.NoError(t, err, "Should open SSH ControlMaster successfully")

	// Verify control socket file exists
	controlPath, err := ssh.DeriveControlPath(host)
	require.NoError(t, err)

	_, err = os.Stat(controlPath)
	require.NoError(t, err, "Control socket file should exist after Open")

	// Test Close
	err = master.Close()
	require.NoError(t, err, "Should close SSH ControlMaster successfully")

	// Verify control socket file is removed
	_, err = os.Stat(controlPath)
	assert.True(t, os.IsNotExist(err), "Control socket file should be removed after Close")
}

// TestSSHMaster_Check tests the health check functionality
func TestSSHMaster_Check(t *testing.T) {
	host := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Open the connection
	err = master.Open(ctx)
	require.NoError(t, err)
	defer master.Close()

	// Test Check when connection is alive
	err = master.Check()
	assert.NoError(t, err, "Check should return nil when connection is healthy")
}

// TestSSHMaster_CheckAfterClose tests that Check fails after Close
func TestSSHMaster_CheckAfterClose(t *testing.T) {
	host := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Open then close
	err = master.Open(ctx)
	require.NoError(t, err)

	err = master.Close()
	require.NoError(t, err)

	// Check should fail after close
	err = master.Check()
	assert.Error(t, err, "Check should return error when connection is closed")
}

// TestSSHMaster_MultipleOpenClose tests opening and closing multiple times
func TestSSHMaster_MultipleOpenClose(t *testing.T) {
	host := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Open and close 3 times
	for i := 0; i < 3; i++ {
		t.Logf("Iteration %d: Opening SSH ControlMaster", i+1)

		err = master.Open(ctx)
		require.NoError(t, err, "Open should succeed on iteration %d", i+1)

		err = master.Check()
		require.NoError(t, err, "Check should succeed after Open on iteration %d", i+1)

		t.Logf("Iteration %d: Closing SSH ControlMaster", i+1)
		err = master.Close()
		require.NoError(t, err, "Close should succeed on iteration %d", i+1)

		// Brief pause between iterations
		time.Sleep(500 * time.Millisecond)
	}
}

// TestSSHMaster_ControlPathReleased tests that control path is properly released
func TestSSHMaster_ControlPathReleased(t *testing.T) {
	host := getTestSSHHost(t)

	logger := logging.NewLogger("debug")

	// First master
	master1, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = master1.Open(ctx)
	require.NoError(t, err)

	controlPath, err := ssh.DeriveControlPath(host)
	require.NoError(t, err)

	// Verify socket exists
	_, err = os.Stat(controlPath)
	require.NoError(t, err, "Socket should exist")

	err = master1.Close()
	require.NoError(t, err)

	// Verify socket is removed
	_, err = os.Stat(controlPath)
	require.True(t, os.IsNotExist(err), "Socket should be removed after Close")

	// Second master should be able to use same path
	master2, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()

	err = master2.Open(ctx2)
	require.NoError(t, err, "Second master should open successfully")
	defer master2.Close()

	// Verify socket exists again
	_, err = os.Stat(controlPath)
	require.NoError(t, err, "Socket should exist for second master")
}

// TestSSHMaster_ConcurrentCheck tests that Check can be called while connection is open
func TestSSHMaster_ConcurrentCheck(t *testing.T) {
	host := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = master.Open(ctx)
	require.NoError(t, err)
	defer master.Close()

	// Call Check multiple times
	for i := 0; i < 5; i++ {
		err = master.Check()
		assert.NoError(t, err, "Check %d should succeed", i+1)
	}
}

// TestSSHMaster_ContextCancellation tests that Open respects context cancellation
func TestSSHMaster_ContextCancellation(t *testing.T) {
	host := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)

	// Create a context that's already canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err = master.Open(ctx)
	assert.Error(t, err, "Open should fail with canceled context")
	assert.Contains(t, err.Error(), "context canceled", "Error should mention context cancellation")
}

// TestSSHMaster_InvalidHost tests error handling for invalid host
func TestSSHMaster_InvalidHost(t *testing.T) {
	logger := logging.NewLogger("debug")

	invalidHosts := []string{
		"user@example.com",     // Missing ssh:// prefix
		"http://user@host.com", // Wrong prefix
		"ssh://",               // Empty after prefix
	}

	for _, host := range invalidHosts {
		t.Run(host, func(t *testing.T) {
			_, err := ssh.NewMaster(host, logger)
			assert.Error(t, err, "Should reject invalid host format: %s", host)
		})
	}
}
