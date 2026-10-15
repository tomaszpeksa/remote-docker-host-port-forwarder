package ssh

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This characterizes the existing internal deadline, not a new retry policy.
// Only the check client stalls: the owned master still answers an independent
// mux request while EnsureAlive holds the lifecycle gate.
func TestMasterSlowCheckClientRetiresResponsiveOwner(t *testing.T) {
	m, dir := masterFixture(t)
	gate, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "client-gate"), Net: "unix"})
	require.NoError(t, err)
	defer gate.Close()
	marker := filepath.Join(dir, "block-client")
	t.Setenv("CHECK_CLIENT_BLOCK", marker)
	t.Setenv("CHECK_CLIENT_GATE", gate.Addr().String())
	require.NoError(t, m.Open(context.Background()))
	original, generation := m.connection, m.generation
	require.NoError(t, os.WriteFile(marker, nil, 0600))
	result := make(chan error, 1)
	go func() { result <- m.EnsureAlive(context.Background()) }()
	require.NoError(t, gate.SetDeadline(time.Now().Add(5*time.Second)))
	blocked, err := gate.Accept()
	require.NoError(t, err)
	defer blocked.Close()
	// Successor startup checks must not inherit the stalled-client condition.
	require.NoError(t, os.Remove(marker))
	probe, err := net.DialTimeout("unix", m.ControlPath(), time.Second)
	require.NoError(t, err)
	defer probe.Close()
	require.NoError(t, probe.SetDeadline(time.Now().Add(time.Second)))
	_, err = fmt.Fprintln(probe, "check")
	require.NoError(t, err)
	reply, err := bufio.NewReader(probe).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("Master running (pid=%d)\n", original.cmd.Process.Pid), reply)
	select {
	case <-original.done:
		t.Fatal("owned master exited before the check client's deadline")
	default:
	}
	select {
	case err = <-result:
		require.NoError(t, err)
	case <-time.After(8 * time.Second):
		t.Fatal("internal check deadline did not complete replacement")
	}
	require.False(t, original == m.connection, "current policy replaces even a responsive owner after a slow check client")
	require.NotNil(t, original.cmd.ProcessState)
	require.Equal(t, generation+1, m.generation)
	require.Equal(t, circuitClosed, m.circuitState)
	require.Zero(t, m.consecutiveFailures)
	require.NoError(t, m.Check())
}

func TestMasterHalfOpenThroughFailedRecreation(t *testing.T) {
	for _, trial := range []string{"success", "failure", "caller-cancel"} {
		t.Run(trial, func(t *testing.T) {
			m, _ := masterFixture(t)
			require.NoError(t, m.Open(context.Background()))
			crashFixtureMaster(t, m)
			// Real owned children exit during startup, leaving temporary sockets
			// for the production retirement path to reclaim on every attempt.
			t.Setenv("S06_EARLY_SOCKET", "1")
			for i := 1; i <= 5; i++ {
				require.ErrorContains(t, m.EnsureAlive(context.Background()), "failed to recreate")
				require.Equal(t, i, m.consecutiveFailures)
				require.False(t, m.ready)
				require.Nil(t, m.connection)
			}
			require.Equal(t, circuitOpen, m.circuitState)
			require.ErrorContains(t, m.EnsureAlive(context.Background()), "circuit breaker open")
			require.Equal(t, 5, m.consecutiveFailures)
			// Only elapsed time is substituted, as in the existing threshold
			// test. No ready/connection/circuit state is fabricated.
			m.lastFailureTime = time.Now().Add(-61 * time.Second)
			switch trial {
			case "failure":
				require.ErrorContains(t, m.EnsureAlive(context.Background()), "failed to recreate")
				require.Equal(t, circuitOpen, m.circuitState)
				require.Equal(t, 6, m.consecutiveFailures)
				require.False(t, m.ready)
				m.lastFailureTime = time.Now().Add(-61 * time.Second)
			case "caller-cancel":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				require.ErrorIs(t, m.EnsureAlive(ctx), context.Canceled)
				require.Equal(t, circuitOpen, m.circuitState, "cancellation is fenced before trial")
				require.Equal(t, 5, m.consecutiveFailures)
				require.False(t, m.ready)
			}
			t.Setenv("S06_EARLY_SOCKET", "")
			require.NoError(t, m.EnsureAlive(context.Background()))
			require.True(t, m.ready)
			require.Equal(t, circuitClosed, m.circuitState)
			require.Zero(t, m.consecutiveFailures)
			connection := m.connection
			require.NoError(t, m.EnsureAlive(context.Background()), "healthy-return path after successful trial")
			require.True(t, connection == m.connection)
			require.Equal(t, circuitClosed, m.circuitState)
			require.Zero(t, m.consecutiveFailures)
			crashFixtureMaster(t, m)
			t.Setenv("S06_EARLY_SOCKET", "1")
			require.ErrorContains(t, m.EnsureAlive(context.Background()), "failed to recreate")
			require.Equal(t, circuitClosed, m.circuitState, "one failed recreation does not reopen after success")
			require.Equal(t, 1, m.consecutiveFailures)
			require.False(t, m.ready)
		})
	}
}
