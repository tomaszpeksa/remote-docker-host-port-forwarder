package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

var ErrPortInUse = errors.New("port already in use")

type PortConflictError struct {
	Port        int
	ContainerID string
	Output      string
}

func (e *PortConflictError) Error() string {
	return fmt.Sprintf("port %d already in use locally", e.Port)
}
func (e *PortConflictError) Unwrap() error { return ErrPortInUse }

func isAddressInUse(err error) bool {
	if err == nil {
		return false
	}
	for _, pattern := range []string{"address already in use", "Address already in use", "cannot listen to port", "bind: Address already in use", "bind ["} {
		if strings.Contains(err.Error(), pattern) {
			return true
		}
	}
	return false
}
func calculateBackoff(attempt int) time.Duration {
	delay := 100 * time.Millisecond
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay > 10*time.Second {
			return 10 * time.Second
		}
	}
	return delay
}

// AddForward requests one exact local TCP tuple on a gate-scoped capability.
// Acknowledgement alone does not prove exclusive ownership of that tuple.
func AddForward(ctx context.Context, connection *Forwarding, localPort, remotePort int, logger *slog.Logger) (ForwardEffect, error) {
	logger.Info("adding SSH port forward", "localPort", localPort, "remotePort", remotePort)
	effect, err := connection.Open(ctx, localPort, remotePort)
	if effect == ForwardRejected && isAddressInUse(err) {
		logger.Warn("port already in use locally", "port", localPort, "remotePort", remotePort, "suggestion", fmt.Sprintf("Check what's using the port with: lsof -i :%d", localPort), "note", "Other forwards will continue normally")
		return effect, &PortConflictError{Port: localPort, Output: err.Error()}
	}
	if effect == ForwardAcknowledged {
		logger.Info("SSH port forward added successfully", "localPort", localPort, "remotePort", remotePort)
	}
	return effect, err
}

// AddForwardWithRetry retains the five-attempt burst only for proven conflict
// rejections. Unknown effects are never replayed; other rejections return intact.
func AddForwardWithRetry(ctx context.Context, connection *Forwarding, localPort, remotePort int, logger *slog.Logger) (ForwardEffect, error) {
	return addForwardWithRetry(ctx, connection, localPort, remotePort, logger, time.After)
}
func addForwardWithRetry(ctx context.Context, connection *Forwarding, localPort, remotePort int, logger *slog.Logger, after func(time.Duration) <-chan time.Time) (ForwardEffect, error) {
	effect := ForwardNotDispatched
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			delay := calculateBackoff(attempt - 1)
			logger.Debug("retrying port forward after backoff", "port", localPort, "attempt", attempt+1, "maxAttempts", 5, "delay", delay.String())
			select {
			case <-after(delay):
			case <-ctx.Done():
				return effect, ctx.Err()
			}
		}
		effect, err = AddForward(ctx, connection, localPort, remotePort, logger)
		if effect != ForwardRejected || !errors.Is(err, ErrPortInUse) {
			return effect, err
		}
	}
	logger.Warn("port forward failed after max retries", "port", localPort, "maxAttempts", 5, "error", err)
	return effect, err
}

// CancelForward requires a correlated positive CLOSE reply. Negative, missing,
// malformed and lost replies must not be interpreted as listener absence.
func CancelForward(ctx context.Context, connection *Forwarding, localPort, remotePort int, logger *slog.Logger) (ForwardEffect, error) {
	logger.Info("canceling SSH port forward", "localPort", localPort, "remotePort", remotePort)
	effect, err := connection.Close(ctx, localPort, remotePort)
	if effect == ForwardAcknowledged {
		logger.Info("SSH port forward canceled successfully", "localPort", localPort, "remotePort", remotePort)
	}
	return effect, err
}
