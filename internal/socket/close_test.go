package socket

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestServerCanceledCloserCannotDeleteSuccessor(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s06-status-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	server, err := NewServer("ssh://owned", state.NewState(), state.NewHistory(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Start(ctx) }()
	cancel()
	require.NoError(t, <-done)
	require.NoError(t, server.Close())
	next, err := net.Listen("unix", server.socketPath)
	require.NoError(t, err)
	defer next.Close()
	require.NoError(t, server.Close())
	_, err = os.Lstat(server.socketPath)
	require.NoError(t, err, "late repeated Close must preserve successor")
}
