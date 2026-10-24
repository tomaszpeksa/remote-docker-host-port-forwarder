package manager

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/muxstub"
)

func TestMuxProcess(t *testing.T) { muxstub.Process(t) }
func recoveryManager(t *testing.T) (*Manager, int, string) {
	t.Helper()
	dir := muxstub.Install(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, h := state.NewState(), state.NewHistory()
	master, err := ssh.NewMaster("ssh://owned-fixture.invalid", logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, master.Close(), "owned Master Wait before fixture cleanup") })
	m := NewManager(&config.Config{Host: "ssh://owned-fixture.invalid"}, nil, reconcile.NewReconciler(st, h, logger), master, st, h, logger)
	return m, listener.Addr().(*net.TCPAddr).Port, filepath.Join(dir, "calls")
}

// Drive real owned generations and the production gate/Apply, rather than
// synthesizing ownership by setting public readiness.
func TestRecoveryInvalidatesOldOwnership(t *testing.T) {
	m, port, calls := recoveryManager(t)
	ctx := context.Background()
	m.state.SetDesired("desired-container", []int{port})
	m.discovery.request = 1
	require.NoError(t, m.sshMaster.Open(ctx))
	require.NoError(t, m.triggerReconcile(ctx))
	created := m.state.GetByContainer("desired-container")[0].CreatedAt
	first, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Contains(t, string(first), "OPEN")
	require.NoError(t, m.triggerReconcile(ctx), "coalesced notification cannot replay owned OPEN")
	after, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, first, after)
	for range 2 {
		require.NoError(t, m.sshMaster.Close())
		require.NoError(t, m.sshMaster.Open(ctx))
		require.NoError(t, m.triggerReconcile(ctx))
		require.Equal(t, "active", m.state.GetByContainer("desired-container")[0].Status)
		require.Equal(t, created, m.state.GetByContainer("desired-container")[0].CreatedAt)
		require.Empty(t, m.history.GetAll(), "no reconnect history")
	}
	after, err = os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, string(first)+string(first)+string(first), string(after))
}

func TestRecoveryQueuedReconciliationFenced(t *testing.T) {
	m, port, calls := recoveryManager(t)
	m.state.SetDesired("desired-container", []int{port})
	m.stopping.Store(true)
	require.ErrorIs(t, m.triggerReconcile(context.Background()), context.Canceled)
	_, err := os.Stat(calls)
	require.True(t, os.IsNotExist(err))
	require.Empty(t, m.state.GetActual())
}
