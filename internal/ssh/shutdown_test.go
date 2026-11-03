package ssh

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/pipehold"
)

type retirementReceiptHandler struct {
	slog.Handler
	observed *atomic.Bool
}

func (h retirementReceiptHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "owned SSH child retired" {
		h.observed.Store(true)
	}
	return h.Handler.Handle(ctx, record)
}

func TestCloseContextRetainsOriginalWaitAfterDeadline(t *testing.T) {
	m, _ := masterFixture(t)
	var receipt atomic.Bool
	m.logger = slog.New(retirementReceiptHandler{Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}), observed: &receipt})
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestLateRetirementProcess$")
	cmd.Env = append(os.Environ(), "S06_LATE_RETIREMENT=1")
	require.NoError(t, cmd.Start())
	c := &masterConnection{cmd: cmd, done: make(chan struct{})}
	m.connection, m.cmd = c, cmd
	release, returned := make(chan struct{}), make(chan struct{})
	var once sync.Once
	complete := func() { once.Do(func() { close(release) }) }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// The original Wait is owned before the closer is launched or any assertion.
	go func() { <-release; c.waitErr = cmd.Wait(); close(c.done) }()
	t.Cleanup(func() { complete(); <-c.done; <-returned })
	var retired bool
	var closeErr error
	go func() { retired, closeErr = m.CloseContext(ctx); close(returned) }()
	<-ctx.Done()
	select {
	case <-returned:
		t.Fatal("close abandoned original Wait when its deadline expired")
	case <-time.After(100 * time.Millisecond):
	}
	require.False(t, receipt.Load(), "retirement receipt preceded original Wait")
	complete()
	<-returned
	require.True(t, receipt.Load(), "retirement receipt must follow actual original Wait proof")
	require.True(t, retired)
	require.ErrorIs(t, closeErr, context.DeadlineExceeded, "late proof must not erase expiry")
	require.NotNil(t, cmd.ProcessState)
	require.Nil(t, m.connection)
	require.Equal(t, uint64(1), m.generation)
}

func TestCloseContextSeparatesRetirementFromNamespaceRefusal(t *testing.T) {
	m, _ := masterFixture(t)
	foreign := filepath.Join(filepath.Dir(m.ControlPath()), "foreign")
	require.NoError(t, os.WriteFile(foreign, []byte("preserve"), 0600))
	t.Cleanup(func() { require.NoError(t, os.Remove(foreign)); require.NoError(t, m.Close()) })
	retired, err := m.CloseContext(context.Background())
	require.True(t, retired, "no child ever owned is retirement proof, not deletion authority")
	require.Error(t, err)
	data, err := os.ReadFile(foreign)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(data))
}

func TestMasterCheckJoinsInheritedDescriptorWithinBudget(t *testing.T) {
	m, dir := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	gate, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "pipe-gate"), Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = gate.Close() })
	require.NoError(t, gate.SetDeadline(time.Now().Add(5*time.Second)))
	t.Setenv("S13_CHECK_PIPE", gate.Addr().String())
	result := make(chan error, 1)
	go func() { result <- m.Check() }()
	var output *os.File
	var finished bool
	t.Cleanup(func() {
		if output != nil {
			_ = output.Close()
		}
		if !finished {
			<-result
		}
	})
	output, err = pipehold.Receive(gate)
	require.NoError(t, err)
	select {
	case err := <-result:
		finished = true
		require.ErrorIs(t, err, exec.ErrWaitDelay)
	case <-time.After(3 * time.Second):
		t.Fatal("SSH check did not join its copier while an inherited descriptor remained open")
	}
}
