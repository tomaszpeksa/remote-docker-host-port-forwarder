package ssh

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCanceledHealthCheckPreservesForwardingLifetime(t *testing.T) {
	for _, mode := range []string{"caller-cancel", "monitor-stop", "internal-timeout"} {
		t.Run(mode, func(t *testing.T) {
			m, dir := masterFixture(t)
			gate, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "check-gate"), Net: "unix"})
			require.NoError(t, err)
			defer gate.Close()
			marker := filepath.Join(dir, "block-check")
			t.Setenv("S06_CHECK_BLOCK", marker)
			t.Setenv("S06_CHECK_GATE", gate.Addr().String())
			require.NoError(t, m.Open(context.Background()))
			original := m.connection
			generation := m.generation
			socket, err := os.Lstat(m.ControlPath())
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(marker, nil, 0600))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			if mode == "monitor-stop" {
				m.StartHealthMonitor(ctx, time.Millisecond)
			} else {
				go func() { result <- m.EnsureAlive(ctx) }()
			}
			require.NoError(t, gate.SetDeadline(time.Now().Add(5*time.Second)))
			blocked, err := gate.Accept()
			require.NoError(t, err)
			defer blocked.Close()
			// Future checks must not block, but this in-flight check stays at the barrier.
			require.NoError(t, os.Remove(marker))
			if mode == "monitor-stop" {
				m.StopHealthMonitor()
			} else {
				if mode == "caller-cancel" {
					cancel()
				}
				select {
				case err = <-result:
				case <-time.After(8 * time.Second):
					t.Fatal("check did not complete")
				}
				if mode == "caller-cancel" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.NoError(t, err)
				}
			}
			blocked.Close()
			if mode == "internal-timeout" {
				require.NotSame(t, original, m.connection, "internal health deadline must still trigger real replacement")
				require.Greater(t, m.generation, generation)
				require.NotNil(t, original.cmd.ProcessState)
			} else {
				require.Same(t, original, m.connection, "operation cancellation must not retire a healthy owner")
				require.Equal(t, generation, m.generation)
				select {
				case <-original.done:
					t.Fatal("healthy retained child was reaped")
				default:
				}
				after, err := os.Lstat(m.ControlPath())
				require.NoError(t, err)
				require.True(t, os.SameFile(socket, after))
				// This is the gate/generation obligation used by Manager to retain forwards.
				require.NoError(t, m.WithConnection(context.Background(), func(g uint64, ready bool) error { require.Equal(t, generation, g); require.True(t, ready); return nil }))
			}
			require.NoError(t, m.Check())
		})
	}
}
