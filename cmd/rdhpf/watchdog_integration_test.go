//go:build rdhpf_integration

package main

import (
	"bytes"
	"debug/buildinfo"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestWatchdogSSHProxy(t *testing.T) {
	if os.Getenv("RDHPF_WATCHDOG_SOCKET") != "" {
		os.Exit(sshfixture.WatchdogProxyMain())
	}
}

type watchdogCLILog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *watchdogCLILog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *watchdogCLILog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buffer.String() }

func watchdogCLITimestamp(text, key string) (time.Time, error) {
	pattern := regexp.MustCompile(regexp.QuoteMeta(key) + `=([^\s]+)`)
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return time.Time{}, fmt.Errorf("missing %s", key)
	}
	return time.Parse(time.RFC3339Nano, strings.Trim(match[1], "\"\n\r"))
}

func TestWatchdogCLITimestampAtLineEnd(t *testing.T) {
	text := "selected_at=2026-10-17T10:13:53.242Z last_successful_event=2026-10-17T10:12:43.461Z\ntime=2026-10-17T10:13:53.243Z level=INFO\n"
	for key, expected := range map[string]string{"selected_at": "2026-10-17T10:13:53.242Z", "last_successful_event": "2026-10-17T10:12:43.461Z"} {
		value, err := watchdogCLITimestamp(text, key)
		require.NoError(t, err, "WATCHDOG_CLI_TIMESTAMP_STOPS_AT_NEWLINE")
		require.Equal(t, expected, value.Format(time.RFC3339Nano))
	}
}

func TestWatchdogOwnedCLISilentStream(t *testing.T) {
	f := sshfixture.NewWatchdog(t)
	binary := os.Getenv("RDHPF_WATCHDOG_BINARY")
	require.True(t, filepath.IsAbs(binary), "actual built CLI required")
	info, err := buildinfo.ReadFile(binary)
	require.NoError(t, err)
	require.Equal(t, "github.com/tomaszpeksa/remote-docker-host-port-forwarder/cmd/rdhpf", info.Path)
	t.Logf("WATCHDOG_CLI_BUILD %s", info.String())
	var logs watchdogCLILog
	cmd := exec.Command(binary, "run", "--host", f.Host, "--log-level", "debug")
	cmd.Stdout = io.MultiWriter(os.Stderr, &logs)
	cmd.Stderr = cmd.Stdout
	cmd.WaitDelay = 2 * time.Second
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	var result error
	go func() { result = cmd.Wait(); close(done) }()
	// No failure path kills the CLI and abandons its Master: request its normal
	// signal shutdown, release controller faults, and retain its Wait completion.
	defer func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		f.Close()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("WATCHDOG_CLI_JOIN_PENDING")
		}
		t.Log("WATCHDOG_CLI_JOINED retained CLI Wait after controller join")
	}()
	require.Eventually(t, func() bool {
		for _, a := range f.Snapshot() {
			if a.Operation == "events" && a.RealChildStarted {
				return true
			}
		}
		return false
	}, 10*time.Second, 10*time.Millisecond)
	id := f.Backend(sshfixture.Alpine, 0)
	require.Eventually(t, func() bool {
		s := logs.String()
		return strings.Contains(s, "containerID="+id[:12]) && strings.Contains(s, "debounce timer fired")
	}, 15*time.Second, 10*time.Millisecond, "actual successful event processing before silence")
	entered, release := f.Gate("events")
	defer release()
	gateAt := time.Now()
	f.Docker("stop", "--time", "0", id)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("owned stream output gate did not enter")
	}
	f.FailAfterCompletedPing()
	select {
	case <-done:
	case <-time.After(80 * time.Second):
		t.Fatal("WATCHDOG_CLI_MUST_EXIT_ON_SILENCE; user cancellation is only failure cleanup")
	}
	var exit *exec.ExitError
	require.ErrorAs(t, result, &exit)
	require.Equal(t, 1, exit.ExitCode())
	text := logs.String()
	require.Contains(t, text, "no successfully handled Docker events")
	extract := func(key string) time.Time {
		value, err := watchdogCLITimestamp(text, key)
		require.NoError(t, err)
		return value
	}
	selected, last := extract("selected_at"), extract("last_successful_event")
	require.True(t, last.Before(gateAt))
	require.GreaterOrEqual(t, selected.Sub(last), 60*time.Second)
	require.Less(t, selected.Sub(last), 71*time.Second)
	canceled := false
	for _, a := range f.Snapshot() {
		if a.Operation == "events" && !a.CanceledAt.IsZero() {
			canceled = true
			require.True(t, a.RealChildStarted)
			require.Less(t, a.CanceledAt.Sub(selected), 3*time.Second)
			t.Logf("WATCHDOG_CLI_STREAM_CANCEL %+v", a)
		}
	}
	require.True(t, canceled)
	release()
	f.Close()
	owner, err := instancepath.Acquire(f.Host)
	require.NoError(t, err, "CLI released publisher admission after joined shutdown")
	require.NoError(t, owner.Close())
	for _, a := range f.Snapshot() {
		if a.Operation != "ping" {
			continue
		}
		raw := f.Docker("events", "--since", a.StartedAt.Add(-time.Second).Format(time.RFC3339Nano), "--until", time.Now().Format(time.RFC3339Nano), "--filter", "container="+a.Name, "--format", "{{.Actor.ID}} {{index .Actor.Attributes \"name\"}}")
		require.Contains(t, raw, a.Name, "actual CLI probe must have a correlated Engine event")
		require.Empty(t, f.Docker("ps", "-aq", "--no-trunc", "--filter", "name=^/"+a.Name+"$"), "normal exact-name CLI probe must be removed")
		t.Logf("WATCHDOG_CLI_PING_ACCOUNT name=%s events=%s", a.Name, raw)
	}
	t.Logf("WATCHDOG_CLI_EXIT code=1 selected=%s last_success=%s observed_wait=%s admission_reacquired=true", selected, last, time.Now())
}
