package ssh

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMasterProcess is an owned, non-network SSH stand-in. Each process has its
// own listener and reports its actual PID; no shared shell invocation counter.
func TestMasterProcess(t *testing.T) {
	if os.Getenv("S06_MASTER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	var path, operation string
	for i, a := range args {
		if a == "-S" {
			path = args[i+1]
		}
		if a == "-O" {
			operation = args[i+1]
		}
		if strings.HasPrefix(a, "ControlPath=") {
			path = strings.TrimPrefix(a, "ControlPath=")
		}
	}
	if operation != "" {
		conn, err := net.Dial("unix", path)
		if err != nil {
			os.Exit(1)
		}
		fmt.Fprintln(conn, operation)
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			os.Exit(1)
		}
		fmt.Fprint(os.Stderr, line)
		conn.Close()
		os.Exit(0)
	}
	if gate := os.Getenv("S06_START_GATE"); gate != "" {
		conn, err := net.Dial("unix", gate)
		if err != nil {
			os.Exit(5)
		}
		fmt.Fprintln(conn, "started")
		_, err = bufio.NewReader(conn).ReadString('\n')
		conn.Close()
		if err != nil {
			os.Exit(6)
		}
	}
	if marker := os.Getenv("S06_START_MARKER"); marker != "" {
		os.WriteFile(marker, []byte(strings.Join(args, "\n")), 0600)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		os.Exit(2)
	}
	defer listener.Close()
	report := func(kind string) {
		if path := os.Getenv("S06_EVENTS"); path != "" {
			conn, err := net.Dial("unix", path)
			if err != nil {
				os.Exit(7)
			}
			_ = json.NewEncoder(conn).Encode(masterEvent{Kind: kind, PID: os.Getpid()})
			conn.Close()
		}
	}
	report("start")
	for {
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(3)
		}
		command, _ := bufio.NewReader(conn).ReadString('\n')
		switch strings.TrimSpace(command) {
		case "check":
			fmt.Fprintf(conn, "Master running (pid=%d)\n", os.Getpid())
			report("check")
		case "crash":
			listener.Close() // unlink before EOF acknowledges the forced loss
			conn.Close()
			os.Exit(9)
		case "exit":
			fmt.Fprintln(conn, "Exit request sent.")
			conn.Close()
			listener.Close()
			os.Exit(0)
		}
		conn.Close()
	}
}

func masterFixture(t *testing.T) (*Master, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "s06-master-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	executable, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestMasterProcess$ -- \"$@\"\n", executable)), 0700))
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	t.Setenv("S06_MASTER_PROCESS", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	t.Setenv("S06_START_MARKER", filepath.Join(dir, "args"))
	m, err := NewMaster("ssh://owned-fixture.invalid", slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	m.controlPath = filepath.Join(dir, "master.sock")
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	return m, dir
}

func TestMasterOwnedForegroundLifetime(t *testing.T) {
	m, dir := masterFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Open(ctx))
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	require.NoError(t, err)
	require.Contains(t, string(args), "ForkAfterAuthentication=no")
	require.Contains(t, string(args), "ControlPersist=no")
	require.NotContains(t, string(args), "-MNf")
	cancel()
	require.NoError(t, m.Check(), "successful startup detaches operation cancellation")
	require.NoError(t, m.Close())
	require.NotNil(t, m.cmd.ProcessState, "Close must observe exactly one completed Wait")
	require.NoError(t, m.Open(context.Background()), "explicit lifecycle reuse")
}

func TestMasterRefusesOccupiedEndpoint(t *testing.T) {
	m, _ := masterFixture(t)
	other, err := net.Listen("unix", m.controlPath)
	require.NoError(t, err)
	defer other.Close()
	require.Error(t, m.Open(context.Background()))
	require.NoError(t, m.Close())
	_, err = os.Lstat(m.controlPath)
	require.NoError(t, err, "foreign endpoint must not be removed")
}

func TestMasterCanceledStartupIsReaped(t *testing.T) {
	m, _ := masterFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Error(t, m.Open(ctx))
	require.NoError(t, m.Close())
	if m.cmd != nil && m.cmd.Process != nil {
		require.NotNil(t, m.cmd.ProcessState)
	}
}

// Keep exec imported here: fixture resolution must never fall back to real SSH.
func TestMasterFixtureResolvesOnlyOwnedExecutable(t *testing.T) {
	_, dir := masterFixture(t)
	path, err := exec.LookPath("ssh")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "ssh"), path)
}

type masterEvent struct {
	Kind string
	PID  int
}

func masterEvents(t *testing.T, dir string) <-chan masterEvent {
	t.Helper()
	path := filepath.Join(dir, "events.sock")
	listener, err := net.Listen("unix", path)
	require.NoError(t, err)
	events := make(chan masterEvent, 1024)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var event masterEvent
			if json.NewDecoder(conn).Decode(&event) == nil {
				events <- event
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	t.Setenv("S06_EVENTS", path)
	return events
}

func awaitMasterEvent(t *testing.T, events <-chan masterEvent, kind string, previousPID int) masterEvent {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.Kind == kind && (previousPID == 0 || event.PID != previousPID) {
				return event
			}
		case <-deadline.C:
			t.Fatalf("no %s event after PID %d", kind, previousPID)
			return masterEvent{}
		}
	}
}

func crashFixtureMaster(t *testing.T, m *Master) {
	t.Helper()
	conn, err := net.Dial("unix", m.ControlPath())
	require.NoError(t, err)
	_, err = fmt.Fprintln(conn, "crash")
	require.NoError(t, err)
	_, err = bufio.NewReader(conn).ReadByte()
	require.Error(t, err)
	conn.Close()
}

func TestMonitorRecoveryKeepsContextAliveTwice(t *testing.T) {
	m, dir := masterFixture(t)
	events := masterEvents(t, dir)
	require.NoError(t, m.Open(context.Background()))
	first := awaitMasterEvent(t, events, "start", 0)
	m.StartHealthMonitor(context.Background(), 5*time.Millisecond)
	defer m.StopHealthMonitor()
	for i := 0; i < 2; i++ {
		crashFixtureMaster(t, m)
		next := awaitMasterEvent(t, events, "start", first.PID)
		awaitMasterEvent(t, events, "check", first.PID) // startup readiness
		awaitMasterEvent(t, events, "check", first.PID) // continuing monitor
		first = next
	}
}

func TestConcurrentRecoveryHasOneReplacement(t *testing.T) {
	m, dir := masterFixture(t)
	events := masterEvents(t, dir)
	require.NoError(t, m.Open(context.Background()))
	first := awaitMasterEvent(t, events, "start", 0)
	crashFixtureMaster(t, m)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; errs <- m.EnsureAlive(context.Background()) }()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	next := awaitMasterEvent(t, events, "start", first.PID)
	for {
		select {
		case event := <-events:
			require.False(t, event.Kind == "start" && event.PID != next.PID, "duplicate replacement")
		default:
			return
		}
	}
}

func TestMasterRacingEndpointIsNotAdoptedOrRemoved(t *testing.T) {
	m, dir := masterFixture(t)
	gate, err := net.Listen("unix", filepath.Join(dir, "gate.sock"))
	require.NoError(t, err)
	defer gate.Close()
	t.Setenv("S06_START_GATE", gate.Addr().String())
	result := make(chan error, 1)
	go func() { result <- m.Open(context.Background()) }()
	gate.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	child, err := gate.Accept()
	require.NoError(t, err)
	defer child.Close()
	_, err = bufio.NewReader(child).ReadString('\n')
	require.NoError(t, err)
	foreign, err := net.Listen("unix", m.ControlPath())
	require.NoError(t, err)
	defer foreign.Close()
	// Keep our child alive at its startup gate: endpoint exists but belongs
	// to this test process, not that directly owned child.
	require.ErrorContains(t, <-result, "does not belong to the owned child")
	require.NoError(t, m.Close())
	require.NotNil(t, m.cmd.ProcessState)
	_, err = os.Lstat(m.ControlPath())
	require.NoError(t, err)
}

func TestMasterCancellationAfterStartReapsChild(t *testing.T) {
	m, dir := masterFixture(t)
	gate, err := net.Listen("unix", filepath.Join(dir, "gate.sock"))
	require.NoError(t, err)
	defer gate.Close()
	t.Setenv("S06_START_GATE", gate.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.Open(ctx) }()
	gate.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	child, err := gate.Accept()
	require.NoError(t, err)
	defer child.Close()
	_, err = bufio.NewReader(child).ReadString('\n')
	require.NoError(t, err)
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.NotNil(t, m.cmd.ProcessState)
	require.NoError(t, m.Close())
}

func TestMasterFailedReopenPublishesRetirement(t *testing.T) {
	m, _ := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	old := m.connection
	m.retirementTrace = diagnosticTrace(t, m)
	diagnosticRecord(t, "owned-before-loss", old.socket, old.socket, old.cmd.Process.Pid, 0, nil)
	crashFixtureMaster(t, m)
	foreign, err := net.Listen("unix", m.ControlPath())
	require.NoError(t, err)
	defer foreign.Close()
	foreignInfo, statErr := os.Lstat(m.ControlPath())
	peerPID := diagnosticPeer(t, m.ControlPath())
	diagnosticRecord(t, "foreign-bound-before-EnsureAlive", old.socket, foreignInfo, old.cmd.Process.Pid, peerPID, statErr)
	ensureErr := m.EnsureAlive(context.Background())
	after, statErr := os.Lstat(m.ControlPath())
	diagnosticRecord(t, "after-EnsureAlive", old.socket, after, old.cmd.Process.Pid, 0, statErr)
	t.Logf("DIAGNOSTIC EnsureAlive error=%v new_owned_pid=%d", ensureErr, m.cmd.Process.Pid)
	require.Error(t, ensureErr)
	require.NoError(t, m.WithConnection(context.Background(), func(generation uint64, ready bool) error {
		require.Equal(t, uint64(1), generation)
		require.False(t, ready)
		return nil
	}))
	require.NotNil(t, m.cmd.ProcessState)
	require.NoError(t, m.Close())
	_, err = os.Lstat(m.ControlPath())
	require.NoError(t, err)
}

func TestMasterApplyGateBlocksReplacementAndFencesShutdown(t *testing.T) {
	m, _ := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	entered, release := make(chan struct{}), make(chan struct{})
	applied := make(chan error, 1)
	go func() {
		applied <- m.WithConnection(context.Background(), func(_ uint64, ready bool) error {
			if !ready {
				return fmt.Errorf("not ready")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	life := m.lifetime.Load()
	closing := make(chan error, 1)
	go func() { closing <- m.Close() }()
	<-life.ctx.Done() // shutdown is fenced before waiting for in-flight Apply
	queued := make(chan error, 1)
	go func() { queued <- m.EnsureAlive(context.Background()) }()
	if m.mu.TryLock() {
		m.mu.Unlock()
		t.Error("Apply must hold the same lifecycle gate")
	}
	close(release)
	require.NoError(t, <-applied)
	require.NoError(t, <-closing)
	require.ErrorIs(t, <-queued, context.Canceled)
	require.NotNil(t, m.cmd.ProcessState)
	require.ErrorIs(t, m.EnsureAlive(context.Background()), context.Canceled)
	require.NoError(t, m.Open(context.Background()))
}

func TestMasterShutdownDuringRecoveryStartup(t *testing.T) {
	m, dir := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	crashFixtureMaster(t, m)
	gate, err := net.Listen("unix", filepath.Join(dir, "gate.sock"))
	require.NoError(t, err)
	defer gate.Close()
	t.Setenv("S06_START_GATE", gate.Addr().String())
	recovering := make(chan error, 1)
	go func() { recovering <- m.EnsureAlive(context.Background()) }()
	gate.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	child, err := gate.Accept()
	require.NoError(t, err)
	defer child.Close()
	_, err = bufio.NewReader(child).ReadString('\n')
	require.NoError(t, err)
	closing := make(chan error, 1)
	go func() { closing <- m.Close() }()
	require.Error(t, <-recovering)
	require.NoError(t, <-closing)
	require.NotNil(t, m.cmd.ProcessState)
	require.Nil(t, m.connection)
}

func TestMasterHealthyCheckDoesNotNotifyReconciliation(t *testing.T) {
	m, _ := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	<-m.Changes()
	require.NoError(t, m.EnsureAlive(context.Background()))
	select {
	case <-m.Changes():
		t.Fatal("healthy checks must not introduce a conflict-retry schedule")
	default:
	}
}

func TestMasterApplyGateBlocksRecovery(t *testing.T) {
	m, dir := masterFixture(t)
	events := masterEvents(t, dir)
	require.NoError(t, m.Open(context.Background()))
	first := awaitMasterEvent(t, events, "start", 0)
	entered, release := make(chan struct{}), make(chan struct{})
	applied := make(chan error, 1)
	go func() {
		applied <- m.WithConnection(context.Background(), func(_ uint64, ready bool) error {
			if !ready {
				return fmt.Errorf("not ready")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	crashFixtureMaster(t, m)
	recovering := make(chan error, 1)
	go func() { recovering <- m.EnsureAlive(context.Background()) }()
	if m.mu.TryLock() {
		m.mu.Unlock()
		t.Error("forward transaction did not hold lifecycle gate")
	}
	select {
	case err := <-recovering:
		t.Errorf("replacement completed during Apply: %v", err)
	default:
	}
	close(release)
	require.NoError(t, <-applied)
	require.NoError(t, <-recovering)
	next := awaitMasterEvent(t, events, "start", first.PID)
	require.NotEqual(t, first.PID, next.PID)
}

func TestMasterCircuitThresholdAndExplicitReuse(t *testing.T) {
	m, _ := masterFixture(t)
	foreign, err := net.Listen("unix", m.ControlPath())
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		require.Error(t, m.EnsureAlive(context.Background()))
	}
	require.Equal(t, circuitOpen, m.circuitState)
	require.Equal(t, 5, m.consecutiveFailures)
	require.ErrorContains(t, m.EnsureAlive(context.Background()), "circuit breaker open")
	require.Equal(t, 5, m.consecutiveFailures)
	foreign.Close()
	// A concurrent cooldown retry is still serialized through the same gate.
	m.lastFailureTime = time.Now().Add(-61 * time.Second)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- m.EnsureAlive(context.Background()) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, circuitClosed, m.circuitState)
	require.NoError(t, m.Close())
	require.NoError(t, m.Open(context.Background()))
	require.NoError(t, m.Close())
	require.NoError(t, m.Close())
}

func TestMasterReapsOwnedChildBeforeRemovingStaleSocket(t *testing.T) {
	m, _ := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	owned := m.connection
	require.NoError(t, owned.cmd.Process.Kill()) // retained child handle, never a reported PID
	<-owned.done
	require.NotNil(t, owned.cmd.ProcessState)
	_, err := os.Lstat(m.ControlPath())
	require.NoError(t, err, "SIGKILL leaves the owned socket")
	require.NoError(t, m.EnsureAlive(context.Background()))
	require.NotSame(t, owned.cmd, m.connection.cmd)
	require.Equal(t, uint64(1), m.generation)
	require.NoError(t, m.Check())
}

func TestMasterUncertainWaitRetainsOwnership(t *testing.T) {
	done := make(chan struct{})
	close(done)
	connection := &masterConnection{cmd: &exec.Cmd{}, done: done, waitErr: fmt.Errorf("wait failed")}
	m := &Master{connection: connection, ready: true}
	require.ErrorContains(t, m.retireConnection(), "did not establish retirement")
	require.Same(t, connection, m.connection)
	require.Zero(t, m.generation)
	require.False(t, m.ready)
}
