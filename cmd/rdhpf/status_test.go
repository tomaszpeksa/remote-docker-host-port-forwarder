package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/spf13/cobra"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/status"
	"golang.org/x/sys/unix"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

func statusHome(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "s15-status-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	t.Setenv("HOME", dir)
}

// The owned peer releases and joins even when the status assertion fails.
// Its finite rescue timer makes the baseline's unbounded decoder fail semantically,
// not by package timeout, while preserving the original handler join.
func statusPeer(t *testing.T, host, payload string, hold bool) {
	t.Helper()
	path, err := socket.GetSocketPath(host)
	require.NoError(t, err)
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	release, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { stop(); _ = listener.Close(); <-done })
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if payload != "" {
			_, _ = conn.Write([]byte(payload))
		}
		if hold {
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
		}
	}()
}

func writeStatus(t *testing.T, host string, snapshot statefile.StateFile) {
	t.Helper()
	path, err := statefile.GetStateFilePath(host)
	require.NoError(t, err)
	data, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0600))
}

func TestStatusSilentSocketHonorsCallerDeadline(t *testing.T) {
	statusHome(t)
	const host = "ssh://status.invalid"
	statusPeer(t, host, "", true)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := getActiveForwards(ctx, host)
	require.Error(t, err, "failed observation cannot become empty success")
	require.Less(t, time.Since(start), 500*time.Millisecond, "caller budget must bound socket decoding")
}

func TestStatusRejectsIncompatibleObservations(t *testing.T) {
	for _, transport := range []string{"file", "socket"} {
		for _, kind := range []string{"missing-version", "unsupported-version", "wrong-host", "corrupt"} {
			t.Run(transport+"/"+kind, func(t *testing.T) {
				statusHome(t)
				const host = "ssh://status.invalid"
				snapshot := statefile.NewSnapshot(host, 1, time.Now(), time.Now(), nil, nil)
				switch kind {
				case "missing-version":
					snapshot.Version = ""
				case "unsupported-version":
					snapshot.Version = "9.0"
				case "wrong-host":
					snapshot.Host = "ssh://private-host.invalid"
				}
				data, err := json.Marshal(snapshot)
				require.NoError(t, err)
				if kind == "corrupt" {
					data = []byte(`{"private-payload":`)
				}
				if transport == "file" {
					path, err := statefile.GetStateFilePath(host)
					require.NoError(t, err)
					require.NoError(t, os.WriteFile(path, data, 0600))
				} else {
					statusPeer(t, host, string(data), false)
				}
				_, err = getActiveForwards(context.Background(), host)
				require.Error(t, err, "invalid observations must not become success")
				require.NotContains(t, err.Error(), "private-")
				require.NotContains(t, err.Error(), os.Getenv("HOME"))
			})
		}
	}
}

func holdStatusFile(t *testing.T, host string, rescue time.Duration) {
	t.Helper()
	path, err := statefile.GetStateFilePath(host)
	require.NoError(t, err)
	f, err := os.Open(path)
	require.NoError(t, err)
	require.NoError(t, unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	release, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() { stop(); <-done })
	go func() {
		defer close(done)
		select {
		case <-release:
		case <-time.After(rescue):
		}
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}()
}

func TestStatusHeldFileLockHonorsCallerDeadline(t *testing.T) {
	statusHome(t)
	const host = "ssh://status.invalid"
	writeStatus(t, host, statefile.NewSnapshot(host, 1, time.Now(), time.Now(), nil, nil))
	holdStatusFile(t, host, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := getActiveForwards(ctx, host)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 500*time.Millisecond)
}

func TestStatusCommandPreservesCallerCancellation(t *testing.T) {
	statusHome(t)
	oldHost, oldFormat := flagHost, flagFormat
	t.Cleanup(func() { flagHost, flagFormat = oldHost, oldFormat })
	flagHost, flagFormat = "ssh://status.invalid", "json"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	require.ErrorIs(t, runStatus(cmd, nil), context.Canceled)
	require.Empty(t, out.String())
}

func TestStatusHistoryStartedEndedAndStructuredLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		snapshot := &statefile.StateFile{
			Forwards: []statefile.ForwardSnapshot{{ContainerID: "current", Port: 8080, Status: "active", CreatedAt: now.Add(-15 * time.Minute)}},
			History:  []statefile.HistorySnapshot{{ContainerID: "history", Port: 9090, FinalStatus: "active", StartedAt: now.Add(-45 * time.Minute), EndedAt: now.Add(-20 * time.Minute), EndReason: "container stopped"}},
		}
		forwards := convertSnapshotToForwards(snapshot)
		require.Equal(t, 15*time.Minute, forwards[0].Duration)
		require.Equal(t, 25*time.Minute, forwards[1].Duration)
		lines := strings.Split(status.FormatTable(forwards), "\n")
		require.Equal(t, []string{"current", "8080", "active", "15m", "ago", "-"}, strings.Fields(lines[2]))
		require.Equal(t, []string{"history", "9090", "stopped", "45m", "ago", "20m", "ago", "container", "stopped"}, strings.Fields(lines[3]))
		for _, output := range []string{status.FormatJSON(forwards), status.FormatYAML(forwards)} {
			require.Contains(t, output, "15m0s")
			require.Contains(t, output, "25m0s")
			require.NotContains(t, output, "45m0s")
		}
	})
}

func TestStatusCancellationInterruptsSocketAndLock(t *testing.T) {
	for _, transport := range []string{"socket", "lock"} {
		t.Run(transport, func(t *testing.T) {
			statusHome(t)
			const host = "ssh://status.invalid"
			if transport == "socket" {
				statusPeer(t, host, `{"version":"2.0",`, true)
			} else {
				writeStatus(t, host, statefile.NewSnapshot(host, 1, time.Now(), time.Now(), nil, nil))
				holdStatusFile(t, host, time.Second)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			timer := time.AfterFunc(50*time.Millisecond, func() { cancel(); close(done) })
			t.Cleanup(func() {
				cancel()
				if !timer.Stop() {
					<-done
				}
			})
			start := time.Now()
			_, err := getActiveForwards(ctx, host)
			require.ErrorIs(t, err, context.Canceled)
			require.Less(t, time.Since(start), 500*time.Millisecond)
		})
	}
}

func TestStateFileStalenessExactBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		snapshot := statefile.StateFile{UpdatedAt: time.Now()}
		time.Sleep(10 * time.Second)
		require.False(t, snapshot.IsStale())
		time.Sleep(time.Nanosecond)
		require.True(t, snapshot.IsStale())
	})
}
