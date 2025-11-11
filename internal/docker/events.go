package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// Event represents a Docker container event
type Event struct {
	// Type is the event type: "start", "die", or "stop"
	Type string

	// ContainerID is the full container ID
	ContainerID string

	// Timestamp is when the event occurred
	Timestamp time.Time
}

// EventReader streams Docker container events via SSH
type EventReader struct {
	sshHost     string
	controlPath string
	logger      *slog.Logger
}

// NewEventReader creates a new Docker event stream reader.
//
// Parameters:
//   - sshHost: SSH connection string in ssh://user@host format
//   - controlPath: Path to SSH control socket
//   - logger: Structured logger for operation logging
//
// Example usage:
//
//	reader := NewEventReader("ssh://user@example.com", "/tmp/rdhpf-abc123.sock", logger)
//	ctx := context.Background()
//	events, errs := reader.Stream(ctx)
func NewEventReader(sshHost, controlPath string, logger *slog.Logger) *EventReader {
	return &EventReader{
		sshHost:     sshHost,
		controlPath: controlPath,
		logger:      logger,
	}
}

// dockerEventJSON represents the JSON structure from docker events
type dockerEventJSON struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	Time     int64  `json:"time"`
	TimeNano int64  `json:"timeNano"`
	Status   string `json:"status"`
}

// Stream starts streaming Docker container events.
// It returns two channels:
//   - events: Channel of Event structs for start, die, and stop events
//   - errors: Channel of errors encountered during streaming
//
// Both channels are closed when the context is canceled or the stream ends.
// The caller should handle errors and restart as needed.
//
// Note: The Manager implements auto-restart logic with exponential backoff
// when the stream fails. This method only starts a single stream.
//
// Example usage:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	events, errs := reader.Stream(ctx)
//	for {
//	    select {
//	    case event, ok := <-events:
//	        if !ok {
//	            return
//	        }
//	        fmt.Printf("Event: %s %s\n", event.Type, event.ContainerID)
//	    case err := <-errs:
//	        log.Printf("Error: %v", err)
//	    }
//	}
func (r *EventReader) Stream(ctx context.Context) (<-chan Event, <-chan error) {
	events := make(chan Event, 10)
	errors := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(errors)

		// Remove ssh:// prefix and parse port for SSH command
		sshHost, port, err := ssh.ParseHost(r.sshHost)
		if err != nil {
			errors <- fmt.Errorf("failed to parse SSH host: %w", err)
			return
		}

		// Build the docker command as a single quoted string to protect {{json .}} from shell expansion
		dockerCmd := `docker events --format '{{json .}}' --filter type=container --filter event=start --filter event=die --filter event=stop`

		// Build SSH command that executes docker via sh -c
		// Important: sh -c and the docker command must be passed as a single argument to SSH
		remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)
		args := []string{"-S", r.controlPath}
		if port != "" {
			args = append(args, "-p", port)
		}
		args = append(args, sshHost, remoteCmd)

		// Log full SSH command before execution
		r.logger.Info("executing docker events command via shell",
			"args", fmt.Sprintf("%v", args),
			"dockerCmd", dockerCmd,
			"host", sshHost,
			"port", port)

		// #nosec G204 - SSH command with validated host format (checked in config.Validate)
		cmd := exec.CommandContext(ctx, "ssh", args...)

		// Capture both stdout and stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			errors <- fmt.Errorf("failed to get stdout pipe: %w", err)
			return
		}

		stderr, err := cmd.StderrPipe()
		if err != nil {
			errors <- fmt.Errorf("failed to get stderr pipe: %w", err)
			return
		}

		if err := cmd.Start(); err != nil {
			errors <- fmt.Errorf("failed to start docker events: %w", err)
			return
		}

		// Ensure process is killed when context is canceled
		go func() {
			<-ctx.Done()
			if cmd.Process != nil {
				// Interrupt the SSH process gracefully
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		}()

		r.logger.Info("docker events stream started",
			"host", sshHost)

		// Buffer to collect stdout lines for error reporting
		var stdoutLines []string
		var stdoutMu sync.Mutex

		// Buffer to collect all stderr output
		var stderrBuf bytes.Buffer
		var wg sync.WaitGroup

		// Read stderr in a separate goroutine
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(&stderrBuf, stderr)
		}()

		// Read and parse JSON lines from stdout
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()

			// Keep last 50 lines for error reporting
			stdoutMu.Lock()
			stdoutLines = append(stdoutLines, line)
			if len(stdoutLines) > 50 {
				stdoutLines = stdoutLines[1:]
			}
			stdoutMu.Unlock()

			var dockerEvent dockerEventJSON
			if err := json.Unmarshal([]byte(line), &dockerEvent); err != nil {
				r.logger.Warn("failed to parse docker event JSON",
					"error", err.Error(),
					"line", line)
				continue
			}

			// Convert to our Event struct
			// Docker events can use either Action or status field
			eventType := dockerEvent.Action
			if eventType == "" {
				eventType = dockerEvent.Status
			}

			// Only process start, die, stop events
			if eventType != "start" && eventType != "die" && eventType != "stop" {
				continue
			}

			event := Event{
				Type:        eventType,
				ContainerID: dockerEvent.Actor.ID,
				Timestamp:   time.Unix(dockerEvent.Time, 0),
			}

			// Send event (non-blocking to handle context cancellation)
			select {
			case events <- event:
				r.logger.Debug("docker event received",
					"type", event.Type,
					"containerID", event.ContainerID[:12])
			case <-ctx.Done():
				return
			}
		}

		// Wait for stderr reader to finish
		wg.Wait()

		// Get stderr content
		stderrContent := stderrBuf.String()
		if stderrContent != "" {
			r.logger.Debug("docker events stderr output",
				"stderr", stderrContent)
		}

		// Check for scanner errors
		if err := scanner.Err(); err != nil {
			r.logger.Warn("docker events scanner error",
				"error", err.Error())
			// Send error to channel so manager can handle it
			select {
			case errors <- fmt.Errorf("scanner error: %w", err):
			case <-ctx.Done():
				return
			}
		}

		// Wait for command to finish
		if err := cmd.Wait(); err != nil {
			// Don't report error if context was canceled
			if ctx.Err() == nil {
				// Extract exit code and signal information
				exitCode := -1
				signalInfo := ""
				if exitErr, ok := err.(*exec.ExitError); ok {
					if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
						exitCode = status.ExitStatus()
						if status.Signaled() {
							signalInfo = fmt.Sprintf("signal: %v", status.Signal())
						}
					}
				}

				// Log detailed failure information
				stdoutMu.Lock()
				stdoutStr := strings.Join(stdoutLines, "\n")
				stdoutMu.Unlock()

				r.logger.Error("docker events command failed",
					"args", fmt.Sprintf("%v", args),
					"error", err.Error(),
					"exitCode", exitCode,
					"signal", signalInfo,
					"stderr", stderrContent,
					"stdoutLines", len(stdoutLines),
					"stdout", stdoutStr)

				// Send error to channel so manager can handle it
				select {
				case errors <- fmt.Errorf("docker events command failed: %w", err):
				case <-ctx.Done():
					return
				}
			}
		}

		r.logger.Info("docker events stream ended",
			"host", sshHost)
	}()

	return events, errors
}
