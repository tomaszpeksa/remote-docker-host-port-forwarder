package manager

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func recoveryManager(t *testing.T) (*Manager, int, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	// Every command invocation appends one short record; never invoke real SSH.
	calls := filepath.Join(dir, "calls")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 0\n", calls)), 0700))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, h := state.NewState(), state.NewHistory()
	m := NewManager(&config.Config{Host: "ssh://owned-fixture.invalid"}, nil, reconcile.NewReconciler(st, h, logger), nil, st, h, logger)
	return m, port, calls
}

// Exercise the exact operation invoked inside Master's gate, including real
// Reconciler.Diff/Apply and SSH argv execution. The TCP fixture is a probe only;
// it is not evidence of remote application traffic.
func TestRecoveryInvalidatesOldOwnership(t *testing.T) {
	m, port, calls := recoveryManager(t)
	m.state.SetDesired("desired-container", []int{port})
	m.state.MarkActive("desired-container", port)
	created := m.state.GetByContainer("desired-container")[0].CreatedAt
	m.state.MarkPending("stopped-container", port+1, "old pending")
	require.Error(t, m.reconcileConnection(context.Background(), 1, false), "failed reopen still invalidates")
	fs := m.state.GetByContainer("desired-container")[0]
	require.Equal(t, "pending", fs.Status)
	require.Equal(t, created, fs.CreatedAt)
	require.Len(t, m.history.GetAll(), 1)
	require.Equal(t, "container stopped", m.history.GetAll()[0].EndReason)
	_, err := os.Stat(calls)
	require.True(t, os.IsNotExist(err), "no commands on unavailable transport")
	require.NoError(t, m.reconcileConnection(context.Background(), 1, true))
	first, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Contains(t, string(first), "-O forward")
	require.Equal(t, "active", m.state.GetByContainer("desired-container")[0].Status)
	require.Equal(t, created, m.state.GetByContainer("desired-container")[0].CreatedAt)
	require.NoError(t, m.reconcileConnection(context.Background(), 1, true), "coalesced/stale wakeup sees current generation")
	after, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, string(first), string(after), "no second Apply for already applied generation")
	require.Len(t, m.history.GetAll(), 1)
	require.NoError(t, m.reconcileConnection(context.Background(), 2, true), "second retirement reapplies")
	after, err = os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, string(first)+string(first), string(after))
	require.Len(t, m.history.GetAll(), 1, "no synthetic reconnect history")
}

func TestRecoveryQueuedReconciliationFenced(t *testing.T) {
	m, port, calls := recoveryManager(t)
	m.state.SetDesired("desired-container", []int{port})
	m.state.MarkActive("desired-container", port)
	m.stopping.Store(true)
	require.ErrorIs(t, m.reconcileConnection(context.Background(), 1, true), context.Canceled)
	require.Equal(t, "active", m.state.GetByContainer("desired-container")[0].Status)
	_, err := os.Stat(calls)
	require.True(t, os.IsNotExist(err))
}
