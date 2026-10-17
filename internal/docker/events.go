package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/util"
)

// Event represents a Docker container event.
type Event struct {
	Type        string
	ContainerID string
	Timestamp   time.Time
}

// StreamResult is published once, after the command and all its readers join.
// Nil Err with Canceled=false means clean EOF, not subscription readiness.
type StreamResult struct {
	Err                  error
	Canceled             bool
	Rejected             uint64
	DiagnosticsTruncated bool
}

// EventReader streams Docker container events via SSH.
type EventReader struct {
	sshHost     string
	controlPath string
	logger      *slog.Logger
}

func NewEventReader(sshHost, controlPath string, logger *slog.Logger) *EventReader {
	return &EventReader{sshHost: sshHost, controlPath: controlPath, logger: logger}
}

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

// Stream owns one command lifetime. Deliver must respect its supplied context;
// an error stops this stream. A caller may backpressure, but must never silently
// discard accepted events. Terminal result does not replace draining its queue.
func (r *EventReader) Stream(ctx context.Context, deliver func(context.Context, Event) error) <-chan StreamResult {
	done := make(chan StreamResult, 1)
	go func() {
		result := r.stream(ctx, deliver)
		done <- result
		close(done)
	}()
	return done
}

func (r *EventReader) stream(ctx context.Context, deliver func(context.Context, Event) error) (result StreamResult) {
	if err := ctx.Err(); err != nil {
		return StreamResult{Err: err, Canceled: true}
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	host, port, err := ssh.ParseHost(r.sshHost)
	if err != nil {
		result.Err = fmt.Errorf("failed to parse SSH host: %w", err)
		return
	}
	dockerCmd := `docker events --format '{{json .}}' --filter type=container --filter event=start --filter event=die --filter event=stop`
	remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)
	args := []string{"-o", "BatchMode=yes", "-S", r.controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", host, remoteCmd)
	r.logger.Info("executing docker events command via shell", "args", fmt.Sprint(args), "dockerCmd", dockerCmd, "host", host, "port", port)
	// #nosec G204 - Fixed SSH executable and separated, validated destination.
	cmd := exec.CommandContext(streamCtx, "ssh", args...)
	// exec owns its OS-pipe copiers; Wait joins them. The extra pipe lets scanner
	// failure close its reader immediately, unblocking the stdout copier. WaitDelay
	// closes inherited OS descriptors, not an excuse to detach an unreaped child.
	reader, writer := io.Pipe()
	defer reader.Close()
	diagnostics := util.NewTailBuffer(64 << 10)
	outputTail := util.NewTailBuffer(64 << 10)
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = writer, diagnostics, 2*time.Second
	started := time.Now()
	if err := cmd.Start(); err != nil {
		_ = writer.Close()
		if ctx.Err() != nil {
			result.Err = ctx.Err()
			result.Canceled = true
		} else {
			result.Err = fmt.Errorf("failed to start docker events: %w", err)
		}
		return
	}
	r.logger.Info("docker events stream started", "host", host, "startup_duration_ms", time.Since(started).Milliseconds(), "pid", cmd.Process.Pid)
	finished := make(chan struct{})
	cleanupDone := watchStreamCleanup(streamCtx, finished, r.logger)
	defer func() { close(finished); <-cleanupDone }()
	waited := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		_ = writer.CloseWithError(waitErr)
		waited <- waitErr
	}()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var deliveryErr error
	var count int
	for scanner.Scan() {
		line := scanner.Bytes()
		_, _ = outputTail.Write(line)
		_, _ = outputTail.Write([]byte{'\n'})
		count++
		var raw dockerEventJSON
		if err := json.Unmarshal(line, &raw); err != nil {
			result.Rejected++
			r.logger.Warn("failed to parse docker event JSON", "error", err.Error(), "line", string(line))
			continue
		}
		kind := raw.Action
		if kind == "" {
			kind = raw.Status
		}
		if kind != "start" && kind != "die" && kind != "stop" {
			continue
		}
		if len(raw.Actor.ID) < 12 {
			result.Rejected++
			r.logger.Warn("rejected malformed docker event ID", "type", kind, "containerID", raw.Actor.ID)
			continue
		}
		event := Event{Type: kind, ContainerID: raw.Actor.ID, Timestamp: time.Unix(raw.Time, 0)}
		if err := deliver(streamCtx, event); err != nil {
			deliveryErr = err
			break
		}
		r.logger.Debug("docker event received", "type", kind, "containerID", event.ContainerID[:12])
	}
	scanErr := scanner.Err()
	if scanErr != nil || deliveryErr != nil {
		cancel()
		_ = reader.Close()
	}
	waitErr := <-waited
	result.DiagnosticsTruncated = diagnostics.Truncated() || outputTail.Truncated()
	if diagnostics.String() != "" {
		r.logger.Debug("docker events stderr output", "stderr", diagnostics.String(), "truncated", diagnostics.Truncated())
	}
	switch {
	case ctx.Err() != nil:
		result.Canceled = true
		result.Err = ctx.Err()
	case deliveryErr != nil:
		result.Err = fmt.Errorf("docker event delivery failed: %w", deliveryErr)
	case scanErr != nil && scanErr != waitErr:
		result.Err = fmt.Errorf("scanner error: %w", scanErr)
	case waitErr != nil:
		result.Err = fmt.Errorf("docker events command failed: %w", waitErr)
	}
	if result.Err != nil && !result.Canceled {
		r.logger.Error("docker events command failed", "error", result.Err, "exitCode", cmd.ProcessState.ExitCode(), "stderr", diagnostics.String(), "stdout", outputTail.String(), "args", fmt.Sprint(args))
	}
	r.logger.Info("docker events stream ended", "host", host, "total_duration_ms", time.Since(started).Milliseconds(), "events_received", count, "rejected", result.Rejected, "diagnostics_truncated", result.DiagnosticsTruncated)
	return
}

func watchStreamCleanup(ctx context.Context, finished <-chan struct{}, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
		}
		logger.Warn("docker events cleanup allowance exceeded; still joining owned workers")
		<-finished
	}()
	return done
}
