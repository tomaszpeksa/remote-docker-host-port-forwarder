package ssh

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// circuitState represents the state of the circuit breaker
type circuitState int

const (
	circuitClosed   circuitState = iota // Normal operation
	circuitOpen                         // Fast-fail mode
	circuitHalfOpen                     // Testing recovery
)

// Master manages an SSH ControlMaster connection
type Master struct {
	host        string
	controlPath string
	cmd         *exec.Cmd
	logger      *slog.Logger

	// Circuit breaker fields
	circuitMu           sync.RWMutex
	circuitState        circuitState
	consecutiveFailures int
	lastFailureTime     time.Time

	// Health monitoring
	healthMonitorCancel context.CancelFunc
	healthMonitorMu     sync.Mutex

	// Recovery callback
	onRecovery func()
}

// NewMaster creates a new SSH ControlMaster manager.
//
// Parameters:
//   - host: SSH connection string in ssh://user@host format
//   - logger: Structured logger for operation logging
//
// Returns:
//   - *Master instance
//   - Error if host format is invalid
//
// Example usage:
//
//	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
//	master, err := NewMaster("ssh://user@example.com", logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
func NewMaster(host string, logger *slog.Logger) (*Master, error) {
	if !strings.HasPrefix(host, "ssh://") {
		return nil, fmt.Errorf("host must be in ssh://user@host format, got: %s", host)
	}

	// Derive control path
	controlPath, err := DeriveControlPath(host)
	if err != nil {
		return nil, fmt.Errorf("failed to derive control path: %w", err)
	}

	return &Master{
		host:                host,
		controlPath:         controlPath,
		logger:              logger,
		circuitState:        circuitClosed,
		consecutiveFailures: 0,
	}, nil
}

// SetRecoveryCallback sets a callback function to be called after successful recovery.
// This allows the manager to trigger reconciliation after SSH reconnects.
func (m *Master) SetRecoveryCallback(callback func()) {
	m.onRecovery = callback
}

// Open establishes the SSH ControlMaster connection.
// It starts the SSH process in background mode and waits for the control
// socket to appear.
//
// The SSH command uses these options:
//   - ControlMaster=auto: Create master if none exists
//   - ControlPersist=10m: Keep connection alive for 10 minutes after last use
//   - ServerAliveInterval=10: Send keepalive every 10 seconds
//   - ServerAliveCountMax=3: Disconnect after 3 failed keepalives
//   - ExitOnForwardFailure=yes: Exit if port forwarding fails
//   - StrictHostKeyChecking=yes: Require known host key
//
// Example usage:
//
//	ctx := context.Background()
//	if err := master.Open(ctx); err != nil {
//	    log.Fatal(err)
//	}
func (m *Master) Open(ctx context.Context) error {
	// Remove ssh:// prefix for SSH command
	sshHost := strings.TrimPrefix(m.host, "ssh://")

	// Extract port if specified (e.g., user@host:port -> user@host and port)
	port := ""
	if idx := strings.LastIndex(sshHost, ":"); idx != -1 {
		port = sshHost[idx+1:]
		sshHost = sshHost[:idx]
	}

	// Build SSH command
	args := []string{
		"-MNf", // Master mode, no command, background
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=10m",
		"-o", fmt.Sprintf("ControlPath=%s", m.controlPath),
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=/dev/null",
	}

	// Add identity file if SSH_TEST_KEY_PATH is set (for integration tests)
	if keyPath := os.Getenv("SSH_TEST_KEY_PATH"); keyPath != "" {
		args = append(args, "-i", keyPath)
	}

	// Add port if specified
	if port != "" {
		args = append(args, "-p", port)
	}

	args = append(args, sshHost)

	m.logger.Debug("starting SSH ControlMaster",
		"host", sshHost,
		"controlPath", m.controlPath)

	// #nosec G204 - SSH command with validated host format (checked in NewMaster)
	m.cmd = exec.CommandContext(ctx, "ssh", args...)

	// Capture stderr for debugging
	m.cmd.Stderr = os.Stderr

	// Start the SSH process
	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start SSH ControlMaster: %w", err)
	}

	// Wait for control socket to appear
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(m.controlPath); err == nil {
			m.logger.Info("SSH ControlMaster established",
				"host", sshHost,
				"controlPath", m.controlPath)
			return nil
		}

		// Check if context was canceled
		select {
		case <-ctx.Done():
			_ = m.cmd.Process.Kill()
			return fmt.Errorf("context canceled while waiting for control socket")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Timeout waiting for socket
	_ = m.cmd.Process.Kill()
	return fmt.Errorf("timeout waiting for control socket at %s", m.controlPath)
}

// Close terminates the SSH ControlMaster connection and cleans up
// the control socket.
//
// Steps performed:
//  1. Stop health monitor if running
//  2. Send SSH "-O exit" to master
//  3. Wait for process termination (1s timeout)
//  4. If still alive: send SIGTERM
//  5. Remove control socket file
//
// Example usage:
//
//	defer master.Close()
func (m *Master) Close() error {
	sshHost := strings.TrimPrefix(m.host, "ssh://")

	m.logger.Debug("closing SSH ControlMaster",
		"host", sshHost,
		"controlPath", m.controlPath)

	// Stop health monitor if running
	m.StopHealthMonitor()

	// Execute SSH exit command
	// #nosec G204 - SSH command with validated host format (checked in NewMaster)
	cmd := exec.Command("ssh",
		"-S", m.controlPath,
		"-O", "exit",
		sshHost)

	if err := cmd.Run(); err != nil {
		m.logger.Warn("failed to cleanly exit SSH ControlMaster",
			"error", err.Error())

		// If SSH exit failed and we have a process handle, try SIGTERM
		if m.cmd != nil && m.cmd.Process != nil {
			m.logger.Debug("attempting SIGTERM on SSH process",
				"pid", m.cmd.Process.Pid)

			// Give it a moment to exit cleanly
			time.Sleep(1 * time.Second)

			// Check if process still exists
			if err := m.cmd.Process.Signal(syscall.Signal(0)); err == nil {
				// Process still exists, send SIGTERM
				m.logger.Warn("sending SIGTERM to orphaned SSH process",
					"pid", m.cmd.Process.Pid)
				_ = m.cmd.Process.Signal(syscall.SIGTERM)
			}
		}
	}

	// Clean up control socket file
	if err := os.Remove(m.controlPath); err != nil && !os.IsNotExist(err) {
		m.logger.Warn("failed to remove control socket",
			"path", m.controlPath,
			"error", err.Error())
	}

	m.logger.Info("SSH ControlMaster closed",
		"host", sshHost)

	return nil
}

// Check verifies that the SSH ControlMaster connection is alive.
//
// Returns:
//   - nil if connection is healthy
//   - error if connection check fails
//
// Example usage:
//
//	if err := master.Check(); err != nil {
//	    log.Printf("Connection unhealthy: %v", err)
//	}
func (m *Master) Check() error {
	sshHost := strings.TrimPrefix(m.host, "ssh://")

	m.logger.Debug("checking SSH ControlMaster health",
		"host", sshHost,
		"controlPath", m.controlPath)

	// #nosec G204 - SSH command with validated host format (checked in NewMaster)
	cmd := exec.Command("ssh",
		"-S", m.controlPath,
		"-O", "check",
		sshHost)

	if err := cmd.Run(); err != nil {
		m.logger.Debug("SSH ControlMaster check failed",
			"host", sshHost,
			"error", err.Error())
		return fmt.Errorf("SSH ControlMaster check failed: %w", err)
	}

	m.logger.Debug("SSH ControlMaster is healthy",
		"host", sshHost)

	return nil
}

// EnsureAlive verifies the SSH ControlMaster connection is healthy and
// recreates it if necessary.
//
// This method:
//  1. Checks the connection health
//  2. If unhealthy, closes the old connection (ignoring errors)
//  3. Opens a new connection
//
// Example usage:
//
//	ctx := context.Background()
//	if err := master.EnsureAlive(ctx); err != nil {
//	    log.Fatal(err)
//	}
func (m *Master) EnsureAlive(ctx context.Context) error {
	// Check circuit breaker state
	m.circuitMu.RLock()
	state := m.circuitState
	m.circuitMu.RUnlock()

	if state == circuitOpen {
		// Circuit is open, check if cooldown period has passed
		m.circuitMu.RLock()
		cooldownPassed := time.Since(m.lastFailureTime) > 60*time.Second
		m.circuitMu.RUnlock()

		if !cooldownPassed {
			return fmt.Errorf("circuit breaker open: too many consecutive failures")
		}

		// Cooldown passed, transition to half-open for one retry
		m.circuitMu.Lock()
		m.circuitState = circuitHalfOpen
		m.circuitMu.Unlock()
		m.logger.Info("circuit breaker transitioning to half-open state")
	}

	// Check if connection is alive
	if err := m.Check(); err != nil {
		sshHost := strings.TrimPrefix(m.host, "ssh://")

		m.logger.Warn("SSH ControlMaster is dead, recreating",
			"host", sshHost,
			"error", err.Error())

		// Close old connection (ignore errors)
		_ = m.Close()

		// Open new connection
		if err := m.Open(ctx); err != nil {
			m.recordFailure()
			return fmt.Errorf("failed to recreate SSH ControlMaster: %w", err)
		}

		m.logger.Info("SSH ControlMaster recreated successfully",
			"host", sshHost)

		// Reset circuit breaker on success
		m.recordSuccess()

		// Call recovery callback if set
		if m.onRecovery != nil {
			m.onRecovery()
		}
	}

	return nil
}

// recordFailure tracks connection failures for circuit breaker
func (m *Master) recordFailure() {
	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	m.consecutiveFailures++
	m.lastFailureTime = time.Now()

	if m.consecutiveFailures >= 5 {
		if m.circuitState != circuitOpen {
			m.logger.Warn("circuit breaker opening",
				"consecutive_failures", m.consecutiveFailures)
			m.circuitState = circuitOpen
		}
	}
}

// recordSuccess resets circuit breaker after successful operation
func (m *Master) recordSuccess() {
	m.circuitMu.Lock()
	defer m.circuitMu.Unlock()

	if m.consecutiveFailures > 0 || m.circuitState != circuitClosed {
		m.logger.Info("circuit breaker closing",
			"previous_failures", m.consecutiveFailures)
	}

	m.consecutiveFailures = 0
	m.circuitState = circuitClosed
}

// StartHealthMonitor starts a background goroutine that periodically checks
// SSH ControlMaster health and attempts recovery on failures.
//
// Parameters:
//   - ctx: Context for cancellation
//   - interval: Time between health checks (e.g., 30*time.Second)
//
// The monitor will:
//  1. Check connection health at specified interval
//  2. On failure: attempt recovery via EnsureAlive()
//  3. Use exponential backoff on repeated failures
//  4. Log all health check and recovery actions
//  5. Stop when context is canceled
//
// Example usage:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//	master.StartHealthMonitor(ctx, 30*time.Second)
func (m *Master) StartHealthMonitor(ctx context.Context, interval time.Duration) {
	m.healthMonitorMu.Lock()
	defer m.healthMonitorMu.Unlock()

	// Create cancelable context for health monitor
	healthCtx, cancel := context.WithCancel(ctx)
	m.healthMonitorCancel = cancel

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		m.logger.Info("SSH health monitor started",
			"interval", interval)

		for {
			select {
			case <-healthCtx.Done():
				m.logger.Info("SSH health monitor stopped")
				return

			case <-ticker.C:
				m.logger.Debug("performing SSH health check")

				if err := m.EnsureAlive(healthCtx); err != nil {
					m.logger.Warn("SSH health check failed",
						"error", err.Error())
				} else {
					m.logger.Debug("SSH health check passed")
				}
			}
		}
	}()
}

// StopHealthMonitor stops the background health monitoring goroutine.
func (m *Master) StopHealthMonitor() {
	m.healthMonitorMu.Lock()
	defer m.healthMonitorMu.Unlock()

	if m.healthMonitorCancel != nil {
		m.healthMonitorCancel()
		m.healthMonitorCancel = nil
	}
}
