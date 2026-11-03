package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// This injected handler observes raw attributes before any formatting/filtering.
type diagnosticHandler struct{ records []slog.Record }

func (*diagnosticHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *diagnosticHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *diagnosticHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *diagnosticHandler) WithGroup(string) slog.Handler      { return h }
func (h *diagnosticHandler) text() string {
	var b bytes.Buffer
	for _, r := range h.records {
		fmt.Fprintln(&b, r.Message)
		r.Attrs(func(a slog.Attr) bool { fmt.Fprintln(&b, a.Key, a.Value); return true })
	}
	return b.String()
}

func TestManagerSafeDiagnostics(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s17-manager-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nprintf private-payload\nprintf private-stderr >&2\nexit \"${S17_EXIT:-7}\"\n"), 0700))
	h := &diagnosticHandler{}
	logger := slog.New(h)
	master, err := ssh.NewMaster("ssh://private-user@192.0.2.77", logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, master.Close()) })
	m := NewManager(&config.Config{Host: "ssh://private-user@192.0.2.77"}, nil, nil, master, state.NewState(), state.NewHistory(), logger)
	err = m.watchdog.dockerPing.Ping(context.Background())
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 7, exit.ExitCode())
	require.Error(t, m.validateDockerConnectivity(context.Background(), "/private-control"))
	t.Setenv("S17_EXIT", "0")
	require.NoError(t, m.watchdog.dockerPing.Ping(context.Background()))
	require.NoError(t, m.validateDockerConnectivity(context.Background(), "/private-control"))
	t.Setenv("S17_EXIT", "7")
	require.NoError(t, m.deliverEvent(context.Background(), 0, docker.Event{Type: "start", ContainerID: "private-unproven-id"}))
	terminal := make(chan docker.StreamResult, 1)
	cause := errors.New("private-cause")
	terminal <- docker.StreamResult{Err: fmt.Errorf("private-wrapper: %w", cause)}
	require.False(t, m.runEventLoop(context.Background(), terminal, 0))
	// Canonical shape in a generic Manager input is still not provenance.
	require.NoError(t, m.handleStopEvent(context.Background(), docker.Event{Type: "stop", ContainerID: identityContainerID}, 0))
	text := h.text()
	require.NotContains(t, text, identityContainerID[:12])
	require.Contains(t, text, "exitCode 7", "actual command exit remains useful without captured output")
	for _, s := range []string{"private-payload", "private-stderr", "private-user", "192.0.2.77", "private-control", "private-unpr", "private-cause", "private-wrapper"} {
		require.NotContains(t, text, s)
	}
	for _, s := range []string{"event health ping docker run failed", "event health ping docker run succeeded", "Docker connectivity test succeeded", "event stream error", "failed to handle start event", "duration_ms"} {
		require.Contains(t, text, s)
	}
}

type failingDiagnosticWriter struct {
	cause  error
	cancel context.CancelFunc
}

func (w failingDiagnosticWriter) Write([]state.ForwardState, []state.HistoryEntry) error {
	if w.cancel != nil {
		w.cancel()
	}
	return w.cause
}
func (w failingDiagnosticWriter) Close() error { return w.cause }

func TestPublicationSafeDiagnostics(t *testing.T) {
	var logs bytes.Buffer
	cause := fmt.Errorf("private-wrapper: %w", os.ErrPermission)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	m := &Manager{state: state.NewState(), history: state.NewHistory(), logger: slog.New(slog.NewTextHandler(&logs, nil)), stateWriter: failingDiagnosticWriter{cause, cancel}}
	err := m.startStateWriter(ctx)
	require.Same(t, cause, err)
	require.ErrorIs(t, err, os.ErrPermission)
	require.Contains(t, logs.String(), "failed to write state")
	require.NotContains(t, logs.String(), "private-wrapper")
}
