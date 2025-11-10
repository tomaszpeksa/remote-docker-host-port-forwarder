package integration

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getTestDockerHost is an alias for getTestSSHHost for status tests
// (kept for compatibility with existing skipped tests)
func getTestDockerHost(t *testing.T) string {
	host := os.Getenv("SSH_TEST_HOST")
	if host == "" {
		t.Skip("Skipping test: SSH_TEST_HOST not set")
	}
	return host
}

func TestStatusCommand_NoActiveForwards(t *testing.T) {
	t.Skip("IMPLEMENTATION PENDING: Status command requires running rdhpf instance. " +
		"Will pass after full implementation.")

	host := getTestDockerHost(t)

	// Run status command (should show no forwards)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "./rdhpf",
		"status",
		"--host", host)

	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "status command should succeed")

	outputStr := string(output)
	assert.Contains(t, outputStr, "No active forwards",
		"should indicate no active forwards")
}

func TestStatusCommand_WithActiveForwards(t *testing.T) {
	t.Skip("IMPLEMENTATION PENDING: Status command requires running rdhpf instance. " +
		"Will pass after full implementation.")

	host := getTestDockerHost(t)

	// Start rdhpf in fixed-ports mode
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start rdhpf with a fixed port
	rdhpfCtx, rdhpfCancel := context.WithCancel(context.Background())
	defer rdhpfCancel()

	cmd := exec.CommandContext(rdhpfCtx, "./rdhpf",
		"run",
		"--host", host,
		"--fixed-ports", "8888")

	output := &strings.Builder{}
	cmd.Stdout = output
	cmd.Stderr = output

	err := cmd.Start()
	require.NoError(t, err, "should start rdhpf")
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// Wait for forward to be established
	time.Sleep(2 * time.Second)

	// Run status command
	statusCmd := exec.CommandContext(ctx, "./rdhpf",
		"status",
		"--host", host)

	statusOutput, err := statusCmd.CombinedOutput()
	require.NoError(t, err, "status command should succeed: %s", string(statusOutput))

	statusStr := string(statusOutput)

	// Should show the forward
	assert.Contains(t, statusStr, "8888", "should show port 8888")
	assert.Contains(t, statusStr, "127.0.0.1:8888", "should show local address")
	assert.Contains(t, statusStr, "active", "should show active state")
	assert.Contains(t, statusStr, "fixed-port-8888", "should show container ID")

	// Stop rdhpf
	rdhpfCancel()
	_ = cmd.Wait()
}

func TestStatusCommand_JSONFormat(t *testing.T) {
	t.Skip("IMPLEMENTATION PENDING: Status command requires running rdhpf instance. " +
		"Will pass after full implementation.")

	host := getTestDockerHost(t)

	// Start rdhpf in fixed-ports mode
	rdhpfCtx, rdhpfCancel := context.WithCancel(context.Background())
	defer rdhpfCancel()

	cmd := exec.CommandContext(rdhpfCtx, "./rdhpf",
		"run",
		"--host", host,
		"--fixed-ports", "7777")

	output := &strings.Builder{}
	cmd.Stdout = output
	cmd.Stderr = output

	err := cmd.Start()
	require.NoError(t, err, "should start rdhpf")
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	// Wait for forward to be established
	time.Sleep(2 * time.Second)

	// Run status command with JSON format
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	statusCmd := exec.CommandContext(ctx, "./rdhpf",
		"status",
		"--host", host,
		"--format", "json")

	statusOutput, err := statusCmd.CombinedOutput()
	require.NoError(t, err, "status command should succeed")

	statusStr := string(statusOutput)

	// Should be valid JSON
	assert.Contains(t, statusStr, `"forwards"`, "should contain forwards array")
	assert.Contains(t, statusStr, `"container_id"`, "should contain container_id field")
	assert.Contains(t, statusStr, "7777", "should show port 7777")

	// Stop rdhpf
	rdhpfCancel()
	_ = cmd.Wait()
}

func TestStatusCommand_YAMLFormat(t *testing.T) {
	t.Skip("IMPLEMENTATION PENDING: Status command requires running rdhpf instance. " +
		"Will pass after full implementation.")

	host := getTestDockerHost(t)

	// Run status command with YAML format (no forwards)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "./rdhpf",
		"status",
		"--host", host,
		"--format", "yaml")

	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "status command should succeed")

	outputStr := string(output)
	assert.Contains(t, outputStr, "forwards:", "should contain YAML forwards field")
}

func TestStatusCommand_MissingHost(t *testing.T) {
	t.Skip("IMPLEMENTATION PENDING: Basic status command test.")

	// Run status command without --host flag
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "./rdhpf", "status")

	output, err := cmd.CombinedOutput()
	assert.Error(t, err, "should fail without --host flag")

	outputStr := string(output)
	assert.Contains(t, outputStr, "required", "should mention required flag")
}

func TestStatusCommand_Performance(t *testing.T) {
	t.Skip("IMPLEMENTATION PENDING: Status command requires running rdhpf instance.")

	host := getTestDockerHost(t)

	// Run status command and measure time
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()

	cmd := exec.CommandContext(ctx, "./rdhpf",
		"status",
		"--host", host)

	_, err := cmd.CombinedOutput()
	require.NoError(t, err, "status command should succeed")

	duration := time.Since(start)

	// Should complete within 500ms (per spec)
	assert.Less(t, duration, 500*time.Millisecond,
		"status command should complete in <500ms, took %v", duration)
}
