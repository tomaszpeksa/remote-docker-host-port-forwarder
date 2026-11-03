package socket

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

type diagnosticConn struct {
	net.Conn
	cause error
}

func (c diagnosticConn) Write([]byte) (int, error) { return 0, c.cause }
func (c diagnosticConn) Close() error              { return c.cause }

func TestServerSafeDiagnostics(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s17-private-path-")
	require.NoError(t, err)
	defer os.RemoveAll(dir)
	var logs bytes.Buffer
	s, err := NewServerAtPath("ssh://private-user@192.0.2.77", filepath.Join(dir, "socket"), state.NewState(), state.NewHistory(), time.Now(), slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.Level(-8)})))
	require.NoError(t, err)
	defer s.Close()
	cause := fmt.Errorf("private-payload: %w", os.ErrPermission)
	s.handleConnection(diagnosticConn{cause: cause})
	require.ErrorIs(t, s.stopErr, os.ErrPermission)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, s.Start(ctx))
	require.ErrorIs(t, s.Close(), os.ErrPermission)
	require.NotContains(t, logs.String(), "private-")
	require.NotContains(t, logs.String(), "192.0.2.77")
	require.Contains(t, logs.String(), "failed to write snapshot to socket")
	require.Contains(t, logs.String(), "socket server listening")
}
