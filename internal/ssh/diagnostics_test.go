package ssh

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMasterForegroundStderrPrivate(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "retirement", true: "startup-failure"}[early], func(t *testing.T) {
			m, dir := masterFixture(t)
			original, err := os.ReadFile(filepath.Join(dir, "ssh"))
			require.NoError(t, err)
			script := "#!/bin/sh\ncase \"$*\" in '-MN '*) i=0; while [ \"$i\" -lt 100 ]; do printf '%1000s' private-child-payload >&2; i=$((i+1)); done; printf private-split >&2; printf -- -unterminated >&2;; esac\n" + strings.TrimPrefix(string(original), "#!/bin/sh\n")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700))
			if early {
				t.Setenv("S06_EARLY_SOCKET", "1")
			}
			capture, err := os.CreateTemp(dir, "stderr")
			require.NoError(t, err)
			before := os.Stderr
			os.Stderr = capture
			// Registered before launch and before any assertion that could abort.
			defer func() { require.NoError(t, m.Close()); os.Stderr = before; require.NoError(t, capture.Close()) }()
			var logs bytes.Buffer
			m.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.Level(-8)}))
			err = m.Open(context.Background())
			if early {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, m.Close())
			require.NotNil(t, m.cmd.ProcessState, "original Wait must join before checking diagnostics")
			var status syscall.WaitStatus
			_, err = syscall.Wait4(m.cmd.Process.Pid, &status, syscall.WNOHANG, nil)
			require.ErrorIs(t, err, syscall.ECHILD)
			require.Nil(t, m.cmd.Stderr, "nil selects os/exec null device; io.Discard adds a copier")
			data, err := os.ReadFile(capture.Name())
			require.NoError(t, err)
			require.Empty(t, string(data))
			require.NotContains(t, logs.String(), dir)
			require.Contains(t, logs.String(), "owned SSH child retired")
			require.Contains(t, logs.String(), "pid=")
		})
	}
}

func TestForwardSafeDiagnostics(t *testing.T) {
	master, dir := masterFixture(t)
	failure := filepath.Join(dir, "fail")
	t.Setenv("S12_MUX_OPEN_FAIL", failure)
	payload := "address already in use private-user@192.0.2.77 /private-control private-payload"
	require.NoError(t, os.WriteFile(failure, []byte(payload), 0600))
	require.NoError(t, master.Open(forwardTestContext(t)))
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.Level(-8)}))
	require.NoError(t, master.WithForwarding(forwardTestContext(t), func(f *Forwarding) error {
		effect, err := addForwardWithRetry(context.Background(), f, 15432, 5432, logger, func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); c <- time.Time{}; return c })
		require.Equal(t, ForwardRejected, effect)
		require.ErrorIs(t, err, ErrPortInUse)
		var conflict *PortConflictError
		require.ErrorAs(t, err, &conflict)
		require.Contains(t, conflict.Output, payload, "private classifier retains original captured rejection")
		return nil
	}))
	require.NotContains(t, logs.String(), "private-")
	require.NotContains(t, logs.String(), "192.0.2.77")
	require.Contains(t, logs.String(), "port=15432")
	require.Contains(t, logs.String(), "maxAttempts=5")
	require.Contains(t, logs.String(), "lsof -i :15432")
}

func TestMasterForegroundCancellationStderrPrivate(t *testing.T) {
	m, dir := masterFixture(t)
	gate, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "stderr-gate"), Net: "unix"})
	require.NoError(t, err)
	defer gate.Close()
	t.Setenv("S06_START_GATE", gate.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	joined := false
	done := make(chan error, 1)
	var child net.Conn
	// Release the fault and join Open's original retirement before fixture cleanup.
	defer func() {
		cancel()
		if child != nil {
			child.Close()
		}
		if !joined {
			<-done
		}
	}()
	go func() { done <- m.Open(ctx) }()
	require.NoError(t, gate.SetDeadline(time.Now().Add(3*time.Second)))
	child, err = gate.Accept()
	require.NoError(t, err)
	require.NoError(t, child.SetDeadline(time.Now().Add(3*time.Second)))
	_, err = bufio.NewReader(child).ReadString('\n')
	require.NoError(t, err)
	cancel()
	err = <-done
	joined = true
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, m.cmd.ProcessState)
	require.Nil(t, m.cmd.Stderr)
	require.NoError(t, m.Close())
}
