package manager

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

// Real Manager retirement, real publication and real admission compose here.
// The existing owned mux peer proves protocol/child ownership, not HTTP traffic.
func TestShutdownStatusHistoryOnceAndSuccessorIntegrity(t *testing.T) {
	m, port, _ := recoveryManager(t)
	owner, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	// Installed before Open/publication/assertions; original Manager joins first.
	t.Cleanup(func() {
		cancel()
		_, shutdownErr := m.Shutdown()
		require.NoError(t, shutdownErr)
		require.NoError(t, owner.Close())
	})
	require.NoError(t, m.Open(ctx))
	m.state.SetDesired("status-owner", []int{port})
	m.discovery.request = 1
	require.NoError(t, m.triggerReconcile(ctx))
	actual := m.state.GetActual()
	require.Len(t, actual, 1)
	created := actual[0].CreatedAt
	socketPath, filePath, err := owner.Paths(m.cfg.Host)
	require.NoError(t, err)
	writer := statefile.NewWriterAtPath(m.cfg.Host, filePath, m.startedAt)
	m.stateWriter = writer
	server, err := socket.NewServerAtPath(m.cfg.Host, socketPath, m.state, m.history, m.startedAt, m.logger)
	require.NoError(t, err)
	m.socketServer = server
	m.shutdown.publishers = m.startPublishers(ctx)
	require.NoError(t, writer.Write(actual, m.history.GetAll()))
	client, err := socket.NewClient(m.cfg.Host)
	require.NoError(t, err)
	live, err := client.GetStatus()
	require.NoError(t, err)
	require.Len(t, live.Forwards, 1)
	require.Equal(t, created.UnixNano(), live.Forwards[0].CreatedAt.UnixNano())
	reader, err := statefile.NewReader(m.cfg.Host)
	require.NoError(t, err)
	stored, err := reader.Read()
	require.NoError(t, err)
	require.Len(t, stored.Forwards, 1)
	require.Empty(t, stored.History)
	deadline, err := m.Shutdown()
	require.NoError(t, err)
	history := m.history.GetAll()
	require.Len(t, history, 1)
	require.Equal(t, created, history[0].StartedAt)
	require.Equal(t, "rdhpf shutdown", history[0].EndReason)
	for range 2 {
		again, e := m.Shutdown()
		require.NoError(t, e)
		require.Equal(t, deadline, again)
	}
	require.Len(t, m.history.GetAll(), 1)
	for _, path := range []string{socketPath, filePath} {
		_, e := os.Lstat(path)
		require.True(t, os.IsNotExist(e), "joined terminal publishers remove old snapshot")
	}
	require.Error(t, writer.Write(actual, history), "retired writer must not publish late")
	require.NoError(t, owner.Close())
	successor, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, successor.Close()) })
	nextWriter := statefile.NewWriterAtPath(m.cfg.Host, filePath, time.Now())
	t.Cleanup(func() { require.NoError(t, nextWriter.Close()) })
	require.NoError(t, nextWriter.Write(nil, history))
	nextServer, err := socket.NewServerAtPath(m.cfg.Host, socketPath, m.state, m.history, time.Now(), m.logger)
	require.NoError(t, err)
	nextCtx, nextCancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	t.Cleanup(func() { nextCancel(); require.NoError(t, nextServer.Close()); require.NoError(t, <-done) })
	go func() { done <- nextServer.Start(nextCtx) }()
	// Repeated old cleanup cannot delete or overwrite a successor's paths.
	_, err = m.Shutdown()
	require.NoError(t, err)
	require.Error(t, writer.Write(actual, nil))
	require.NoError(t, server.Close())
	for _, read := range []func() (*statefile.StateFile, error){reader.Read, client.GetStatus} {
		snapshot, e := read()
		require.NoError(t, e)
		require.Empty(t, snapshot.Forwards)
		require.Len(t, snapshot.History, 1)
		require.Equal(t, "status-owner", snapshot.History[0].ContainerID)
		require.Equal(t, created.UnixNano(), snapshot.History[0].StartedAt.UnixNano())
		require.Equal(t, "rdhpf shutdown", snapshot.History[0].EndReason)
	}
}
