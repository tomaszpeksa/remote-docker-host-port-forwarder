package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// ErrPortInUse indicates that a local port is already in use and cannot be bound
var ErrPortInUse = errors.New("port already in use")

// PortConflictError wraps ErrPortInUse with additional context
type PortConflictError struct {
	Port        int
	ContainerID string
	Output      string
}

func (e *PortConflictError) Error() string {
	return fmt.Sprintf("port %d already in use locally", e.Port)
}

func (e *PortConflictError) Unwrap() error {
	return ErrPortInUse
}

// isAddressInUse checks if an error indicates that an address/port is already in use
func isAddressInUse(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// SSH returns various error messages for port conflicts
	patterns := []string{
		"address already in use",
		"Address already in use",
		"cannot listen to port",
		"remote port forwarding failed",
		"bind: Address already in use",
		"bind [",
	}
	for _, pattern := range patterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}
	return false
}

// calculateBackoff calculates exponential backoff delay for retry attempts
// Base delay: 100ms, exponential factor: 2, max delay: 10s
func calculateBackoff(attempt int) time.Duration {
	baseDelay := 100 * time.Millisecond
	maxDelay := 10 * time.Second

	// Calculate exponential backoff: baseDelay * 2^attempt
	delay := baseDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
			break
		}
	}

	return delay
}

// AddForwardWithRetry attempts to add a port forward with retry logic for conflicts.
// It implements exponential backoff when encountering "port already in use" errors.
//
// T050: Retry with exponential backoff
// - Initial delay: 100ms
// - Max delay: 10s
// - Max attempts: 5
// - Only retries on ErrPortInUse
//
// Parameters:
//   - ctx: Context for cancellation
//   - controlPath: Path to SSH control socket
//   - host: SSH connection string in ssh://user@host format
//   - localPort: Local port to bind (on 127.0.0.1)
//   - remotePort: Remote port to forward from (on remote 127.0.0.1)
//   - logger: Structured logger for operation logging
//
// Returns:
//   - nil on success
//   - *PortConflictError if port remains in use after all retries
//   - other error if different failure occurs
func AddForwardWithRetry(ctx context.Context, controlPath, host string, localPort, remotePort int, logger *slog.Logger) error {
	const maxAttempts = 5

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Calculate and apply backoff
			delay := calculateBackoff(attempt - 1)
			logger.Debug("retrying port forward after backoff",
				"port", localPort,
				"attempt", attempt+1,
				"maxAttempts", maxAttempts,
				"delay", delay.String())

			select {
			case <-time.After(delay):
				// Continue to retry
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		err := AddForward(ctx, controlPath, host, localPort, remotePort, logger)
		if err == nil {
			if attempt > 0 {
				logger.Info("port forward succeeded after retry",
					"port", localPort,
					"attempts", attempt+1)
			}
			return nil
		}

		lastErr = err

		// Only retry on port conflicts
		var portErr *PortConflictError
		if !errors.As(err, &portErr) {
			// Non-retryable error
			return err
		}

		// Log retry attempt for conflicts
		if attempt < maxAttempts-1 {
			logger.Debug("port conflict detected, will retry",
				"port", localPort,
				"attempt", attempt+1,
				"maxAttempts", maxAttempts)
		}
	}

	// Max retries reached
	logger.Warn("port forward failed after max retries",
		"port", localPort,
		"maxAttempts", maxAttempts,
		"error", lastErr.Error())

	return lastErr
}

// AddForward adds an SSH port forward via the ControlMaster connection.
//
// This executes: ssh -S {controlPath} -O forward -L 127.0.0.1:{localPort}:127.0.0.1:{remotePort} {host}
//
// Parameters:
//   - ctx: Context for cancellation
//   - controlPath: Path to SSH control socket
//   - host: SSH connection string in ssh://user@host format
//   - localPort: Local port to bind (on 127.0.0.1)
//   - remotePort: Remote port to forward from (on remote 127.0.0.1)
//   - logger: Structured logger for operation logging
//
// Returns:
//   - nil on success
//   - error if the forward could not be established
//
// Example usage:
//
//	ctx := context.Background()
//	err := AddForward(ctx, "/tmp/rdhpf-abc.sock", "ssh://user@host", 5432, 5432, logger)
//	if err != nil {
//	    log.Fatal(err)
//	}
func AddForward(ctx context.Context, controlPath, host string, localPort, remotePort int, logger *slog.Logger) error {
	// Parse host and port from SSH URL
	sshHost, port, err := ParseHost(host)
	if err != nil {
		return fmt.Errorf("failed to parse SSH host: %w", err)
	}

	// Build SSH command - port flag must come before control operations
	// Use "localhost" for remote side so it can be resolved via /etc/hosts in test environments
	forwardSpec := fmt.Sprintf("127.0.0.1:%d:localhost:%d", localPort, remotePort)
	args := []string{"-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "-O", "forward", "-L", forwardSpec, sshHost)

	logger.Info("adding SSH port forward",
		"localPort", localPort,
		"remotePort", remotePort,
		"host", sshHost)

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	if output, err := cmd.CombinedOutput(); err != nil {
		outputStr := strings.TrimSpace(string(output))

		// Check if this is a port conflict error
		if isAddressInUse(err) || isAddressInUse(errors.New(outputStr)) {
			// T048: Detect port conflict
			// T049: Log with actionable guidance
			logger.Warn("port already in use locally",
				"port", localPort,
				"remotePort", remotePort,
				"suggestion", fmt.Sprintf("Check what's using the port with: lsof -i :%d", localPort),
				"note", "Other forwards will continue normally")

			return &PortConflictError{
				Port:   localPort,
				Output: outputStr,
			}
		}

		// Other errors
		return fmt.Errorf("failed to add port forward %d->%d: %w (output: %s)",
			localPort, remotePort, err, outputStr)
	}

	logger.Info("SSH port forward added successfully",
		"localPort", localPort,
		"remotePort", remotePort)

	return nil
}

// CancelForward removes an SSH port forward via the ControlMaster connection.
//
// This executes: ssh -S {controlPath} -O cancel -L 127.0.0.1:{localPort}:127.0.0.1:{remotePort} {host}
//
// This operation is graceful - it does not error if the forward is already removed.
//
// Parameters:
//   - ctx: Context for cancellation
//   - controlPath: Path to SSH control socket
//   - host: SSH connection string in ssh://user@host format
//   - localPort: Local port that was bound
//   - remotePort: Remote port that was forwarded from
//   - logger: Structured logger for operation logging
//
// Returns:
//   - nil on success (or if forward was already gone)
//   - error only on unexpected failures
//
// Example usage:
//
//	ctx := context.Background()
//	err := CancelForward(ctx, "/tmp/rdhpf-abc.sock", "ssh://user@host", 5432, 5432, logger)
//	if err != nil {
//	    log.Printf("Warning: %v", err)
//	}
func CancelForward(ctx context.Context, controlPath, host string, localPort, remotePort int, logger *slog.Logger) error {
	// Parse host and port from SSH URL
	sshHost, port, err := ParseHost(host)
	if err != nil {
		return fmt.Errorf("failed to parse SSH host: %w", err)
	}

	// Build SSH command - port flag must come before control operations
	// Use "localhost" for remote side so it can be resolved via /etc/hosts in test environments
	forwardSpec := fmt.Sprintf("127.0.0.1:%d:localhost:%d", localPort, remotePort)
	args := []string{"-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "-O", "cancel", "-L", forwardSpec, sshHost)

	logger.Info("canceling SSH port forward",
		"localPort", localPort,
		"remotePort", remotePort,
		"host", sshHost)

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	if output, err := cmd.CombinedOutput(); err != nil {
		outputStr := strings.TrimSpace(string(output))

		// Check if error indicates forward was already gone
		// SSH returns error when trying to cancel non-existent forward
		if strings.Contains(outputStr, "No such") ||
			strings.Contains(outputStr, "not found") ||
			strings.Contains(outputStr, "does not exist") {
			logger.Debug("port forward already removed",
				"localPort", localPort,
				"remotePort", remotePort)
			return nil
		}

		// Log other errors but don't fail the operation
		logger.Warn("failed to cancel port forward (non-critical)",
			"localPort", localPort,
			"remotePort", remotePort,
			"error", err.Error(),
			"output", outputStr)
		return nil
	}

	logger.Info("SSH port forward canceled successfully",
		"localPort", localPort,
		"remotePort", remotePort)

	return nil
}
