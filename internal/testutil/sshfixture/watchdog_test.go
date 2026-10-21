//go:build rdhpf_integration

package sshfixture

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func inertWatchdog(t *testing.T) *WatchdogFixture {
	t.Helper()
	dir := t.TempDir()
	ssh := filepath.Join(dir, "ssh")
	require.NoError(t, os.WriteFile(ssh, []byte("#!/bin/sh\nprintf entered > \"$RDHPF_WATCHDOG_CHILD_MARKER\"\nprintf 'owned-output\\n'\n"), 0700))
	t.Setenv("RDHPF_WATCHDOG_CHILD_MARKER", filepath.Join(dir, "child-entered"))
	return newWatchdogController(t, &Fixture{T: t, Alias: "rdhpf-trust-0123456789abcdef", SSH: ssh, Dir: dir})
}
func watchdogConnection(t *testing.T, w *WatchdogFixture, request watchdogRequest) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", w.socket)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, json.NewEncoder(conn).Encode(request))
	return conn
}
func TestWatchdogFixtureRefusesUnownedRequests(t *testing.T) {
	for _, tc := range []struct{ name, operation, destination, command string }{
		{"destination", "ping", "foreign", "docker run --rm --name rdhpf-ping-1 --label rdhpf.health-check=true alpine:latest echo works"},
		{"operation", "unknown", "rdhpf-trust-0123456789abcdef", watchdogEvents},
		{"command", "ping", "rdhpf-trust-0123456789abcdef", "arbitrary command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := inertWatchdog(t)
			conn := watchdogConnection(t, w, watchdogRequest{Operation: tc.operation, Args: []string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "--", tc.destination, tc.command}})
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
			var frame watchdogFrame
			require.NoError(t, json.NewDecoder(conn).Decode(&frame))
			require.NotNil(t, frame.Exit)
			require.Equal(t, 97, *frame.Exit)
			require.Empty(t, w.Snapshot())
			require.NoFileExists(t, os.Getenv("RDHPF_WATCHDOG_CHILD_MARKER"))
			w.Close()
		})
	}
}
func TestWatchdogFixtureFiniteEventsValidatedOnce(t *testing.T) {
	w := inertWatchdog(t)
	argsFile := filepath.Join(w.Dir, "finite-argv")
	t.Setenv("STREAM_FINITE_ARGV", argsFile)
	require.NoError(t, os.WriteFile(w.SSH, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$STREAM_FINITE_ARGV\"\nexit 7\n"), 0700))
	since, until := time.Unix(100, 0), time.Unix(101, 0)
	w.FiniteEvents(since, until, 7)
	base := []string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "--", w.Alias}
	request := watchdogRequest{Operation: "events", Args: append(append([]string(nil), base...), watchdogEvents+"; unowned")}
	conn := watchdogConnection(t, w, request)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	var frame watchdogFrame
	require.NoError(t, json.NewDecoder(conn).Decode(&frame))
	require.NotNil(t, frame.Exit)
	require.Equal(t, 97, *frame.Exit, "FINITE_STREAM_MUST_VALIDATE_BEFORE_INJECTION")
	require.Empty(t, w.Snapshot())
	require.NoFileExists(t, argsFile)
	require.NoError(t, conn.Close())
	for index := range 2 {
		request.Args = append(append([]string(nil), base...), watchdogEvents)
		conn = watchdogConnection(t, w, request)
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
		require.NoError(t, json.NewDecoder(conn).Decode(&frame))
		require.NotNil(t, frame.Exit)
		require.Equal(t, 7, *frame.Exit, "FINITE_STREAM_MUST_PRESERVE_ACTUAL_EXIT")
		require.NoError(t, conn.Close())
		data, err := os.ReadFile(argsFile)
		require.NoError(t, err)
		args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		require.Equal(t, base, args[:len(args)-1], "finite injection preserves trust/options/destination")
		if index == 0 {
			command := "docker events --format '{{json .}}' --filter type=container --filter event=start --filter event=die --filter event=stop --since " + watchdogShellQuote(since.Format(time.RFC3339Nano)) + " --until " + watchdogShellQuote(until.Format(time.RFC3339Nano)) + " && exit 7"
			require.Equal(t, "sh -c "+watchdogShellQuote(command), args[len(args)-1])
		} else {
			require.Equal(t, watchdogEvents, args[len(args)-1], "finite stimulus consumed once")
		}
	}
	w.Close()
	a := w.Snapshot()
	require.Len(t, a, 2)
	require.True(t, a[0].FiniteEvents)
	require.False(t, a[1].FiniteEvents)
	for _, attempt := range a {
		require.Equal(t, 7, attempt.ExitCode)
		require.False(t, attempt.JoinedAt.IsZero())
		require.False(t, attempt.CompletedAt.IsZero())
	}
}

func TestWatchdogFixtureProxyDeathJoinsHeldChild(t *testing.T) {
	w := inertWatchdog(t)
	entered, release := w.Gate("events")
	defer release()
	conn := watchdogConnection(t, w, watchdogRequest{Operation: "events", Args: []string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "--", w.Alias, watchdogEvents}, PID: os.Getpid()})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("actual child did not reach output gate")
	}
	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool {
		a := w.Snapshot()
		return len(a) == 1 && !a[0].CanceledAt.IsZero() && !a[0].JoinedAt.IsZero()
	}, time.Second, time.Millisecond, "CONTROLLER_MUST_CANCEL_AND_WAIT_INDEPENDENT_OF_GATE")
	a := w.Snapshot()[0]
	require.True(t, a.RealChildStarted)
	require.NotZero(t, a.ChildPID)
	require.True(t, a.CompletedAt.IsZero(), "held completion is separately owned")
	release()
	w.Close()
	require.False(t, w.Snapshot()[0].CompletedAt.IsZero())
}
func TestWatchdogFixtureCanceledDispatchNeverLaunches(t *testing.T) {
	w := inertWatchdog(t)
	entered, release := w.GatePingDispatch()
	defer release()
	conn := watchdogConnection(t, w, watchdogRequest{Operation: "ping", Args: []string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "--", w.Alias, "docker run --rm --name rdhpf-ping-123 --label rdhpf.health-check=true alpine:latest echo works"}, PID: os.Getpid()})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("dispatch hold not entered")
	}
	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool { return !w.Snapshot()[0].CanceledAt.IsZero() }, time.Second, time.Millisecond)
	release()
	w.Close()
	a := w.Snapshot()[0]
	require.True(t, a.DispatchSuppressed)
	require.False(t, a.RealChildStarted)
	require.Empty(t, a.ContainerID)
	require.Zero(t, a.ChildPID)
	require.NoFileExists(t, os.Getenv("RDHPF_WATCHDOG_CHILD_MARKER"), "CANCELED_DISPATCH_MUST_NOT_LAUNCH")
}
func TestWatchdogFixtureFailureChild(t *testing.T) {
	if os.Getenv("RDHPF_WATCHDOG_FAILURE_CHILD") != "1" {
		return
	}
	w := inertWatchdog(t)
	entered, _ := w.Gate("events")
	conn := watchdogConnection(t, w, watchdogRequest{Operation: "events", Args: []string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "--", w.Alias, watchdogEvents}})
	<-entered
	_ = conn.Close()
	w.Close()
	t.Error("INTENTIONAL_AFTER_JOIN")
}
func TestWatchdogFixtureAssertionFailureCleanup(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.v", "-test.run", "^TestWatchdogFixtureFailureChild$")
	cmd.Env = append(os.Environ(), "RDHPF_WATCHDOG_FAILURE_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	require.Contains(t, string(output), "INTENTIONAL_AFTER_JOIN")
	require.Contains(t, string(output), "WATCHDOG_FIXTURE_JOINED")
	require.Less(t, strings.Index(string(output), "WATCHDOG_FIXTURE_JOINED"), strings.Index(string(output), "INTENTIONAL_AFTER_JOIN"))
}

// These negatives exercise the actual classifier used before either exec path.
func TestWatchdogFixtureRejectsCommandInjection(t *testing.T) {
	const alias = "rdhpf-trust-0123456789abcdef"
	for _, command := range []string{
		"docker events --format json; touch /tmp/unowned",
		"echo docker version --format; arbitrary-command",
		"docker inspect --format '{{json .HostConfig.PortBindings}}' -- bad; arbitrary-command",
		"docker ps --format anything; arbitrary-command",
	} {
		_, _, _, err := watchdogOperation([]string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "--", alias, command}, alias)
		require.Error(t, err, "WATCHDOG_REFUSES_NONEXACT_COMMAND: %s", command)
	}
	for _, options := range [][]string{
		{"-o", "ProxyCommand=unowned"}, {"-F", "/tmp/unowned"}, {"-o", "BatchMode=no"},
	} {
		args := append(options, "--", alias, "docker run --rm --name rdhpf-ping-123 --label rdhpf.health-check=true alpine:latest echo works")
		_, _, _, err := watchdogOperation(args, alias)
		require.Error(t, err, "WATCHDOG_REFUSES_UNOWNED_OPTIONS")
	}
}

func TestWatchdogFixtureAllowsConcreteApplicationCommands(t *testing.T) {
	const alias = "rdhpf-trust-0123456789abcdef"
	id := strings.Repeat("a", 64)
	for _, tc := range []struct{ command, operation string }{
		{watchdogEvents, "events"},
		{"docker run --rm --name rdhpf-ping-123 --label rdhpf.health-check=true alpine:latest echo works", "ping"},
		{`sh -c "docker ps --no-trunc --format '{{.ID}}'"`, "other"},
		{`sh -c "docker version --format '{{.Server.Version}}'"`, "other"},
		{"sh -c " + watchdogShellQuote("docker inspect --format '{{json .HostConfig.PortBindings}}' -- '"+id+"'"), "inspect"},
		{"sh -c " + watchdogShellQuote("docker inspect --format '{{json .Config.Labels}}' -- '"+id+"'"), "other"},
		{"sh -c " + watchdogShellQuote("docker inspect --format '{{index .Config.Labels \"rdhpf.test-infrastructure\"}}' -- '"+id+"'"), "other"},
	} {
		operation, _, _, err := watchdogOperation([]string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "--", alias, tc.command}, alias)
		require.NoError(t, err, tc.command)
		require.Equal(t, tc.operation, operation)
	}
	for _, op := range []string{"check", "forward", "cancel"} {
		args := []string{"-o", "BatchMode=yes", "-S", "/tmp/owned/master.sock", "-O", op}
		if op != "check" {
			args = append(args, "-L", "127.0.0.1:1234:localhost:80")
		}
		operation, _, _, err := watchdogOperation(append(args, "--", alias), alias)
		require.NoError(t, err)
		require.Equal(t, "other", operation)
	}
	operation, _, _, err := watchdogOperation([]string{"-MN", "-o", "ControlMaster=yes", "-o", "ForkAfterAuthentication=no", "-o", "ControlPersist=no", "-o", "ControlPath=/tmp/owned/master.sock", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-o", "TCPKeepAlive=yes", "-o", "ExitOnForwardFailure=yes", "-o", "BatchMode=yes", "--", alias}, alias)
	require.NoError(t, err)
	require.Equal(t, "other", operation)
}

func TestWatchdogFixtureProxyMarkerAndProvider(t *testing.T) {
	w := inertWatchdog(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("RDHPF_WATCHDOG_TEST_EXECUTABLE", executable)
	require.True(t, watchdogProxyEnvironment(w.socket, w.Alias, "/usr/bin/ssh"))
	require.False(t, watchdogProxyEnvironment(w.socket, w.Alias, w.SSH), "arbitrary absolute executable is not native OpenSSH")
	require.False(t, watchdogProxyEnvironment(w.socket, "foreign", "/usr/bin/ssh"))
	require.False(t, watchdogProxyEnvironment("relative", w.Alias, "/usr/bin/ssh"))
	t.Setenv("RDHPF_WATCHDOG_TEST_EXECUTABLE", "/usr/bin/ssh")
	require.False(t, watchdogProxyEnvironment(w.socket, w.Alias, "/usr/bin/ssh"), "marker must identify this test executable")
}
