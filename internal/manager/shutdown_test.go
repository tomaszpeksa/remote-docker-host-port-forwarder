package manager

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/pipehold"
)

func TestRunAdmissionFailureRetiresConstructedMaster(t *testing.T) {
	m, _, _ := recoveryManager(t)
	dir := filepath.Dir(m.sshMaster.ControlPath())
	require.Error(t, m.Run(context.Background(), nil))
	_, err := os.Lstat(dir)
	require.True(t, os.IsNotExist(err), "every Run exit must retire the already-owned Master namespace")
}

func TestRunCanceledConsumesActualHistoryOnce(t *testing.T) {
	m, port, _ := recoveryManager(t)
	owner, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	defer owner.Close()
	m.state.SetDesired("actual-owner", []int{port})
	m.state.MarkPending("actual-owner", port, "waiting")
	m.state.SetResource(state.ForwardResource{ContainerID: "actual-owner", Port: port, RemotePort: port, Ownership: state.ForwardUnknown, EndReason: "container stopped"})
	m.state.SetDesired("desired-only", []int{12345})
	created := m.state.GetByContainer("actual-owner")[0].CreatedAt
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, m.Run(ctx, owner), context.Canceled)
	history := m.history.GetAll()
	require.Len(t, history, 1, "terminal owner must consume actual records even before monitoring starts")
	require.Equal(t, created, history[0].StartedAt)
	require.Equal(t, "container stopped", history[0].EndReason)
	require.Empty(t, m.state.GetActual())
	require.Empty(t, m.state.GetResources())
	require.Empty(t, m.state.GetDesired())
}

func TestShutdownDeadlineRetainsPublicationAndAdmission(t *testing.T) {
	m, _, _ := recoveryManager(t)
	owner, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	defer owner.Close()
	_, statePath, err := owner.Paths(m.cfg.Host)
	require.NoError(t, err)
	synctest.Test(t, func(t *testing.T) {
		blocked := &blockedFinalWriter{real: statefile.NewWriterAtPath(m.cfg.Host, statePath, m.startedAt), entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
		var once sync.Once
		release := func() { once.Do(func() { close(blocked.release) }) }
		m.stateWriter = blocked
		t.Cleanup(func() { release(); _, _ = m.Shutdown() })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		m.shutdown.publishers = m.startPublishers(ctx)
		m.requestShutdown()
		<-blocked.entered
		first := m.shutdown.deadline
		returned := make(chan struct{})
		var deadline time.Time
		var result error
		go func() { deadline, result = m.Shutdown(); close(returned) }()
		time.Sleep(11 * time.Second)
		m.requestShutdown()
		require.Equal(t, first, m.shutdown.deadline, "repeated requests must not renew the first budget")
		select {
		case <-returned:
			t.Fatal("expiry abandoned the blocked final publication")
		default:
		}
		contender, admissionErr := instancepath.Acquire(m.cfg.Host)
		if contender != nil {
			_ = contender.Close()
		}
		require.Error(t, admissionErr, "successor admitted while a publisher still owns completion")
		release()
		<-returned
		require.Equal(t, first, deadline)
		require.ErrorIs(t, result, context.DeadlineExceeded)
		laterDeadline, laterErr := m.Shutdown()
		require.Equal(t, first, laterDeadline)
		require.Same(t, result, laterErr, "late cleanup cannot erase the sticky failed result")
	})
	require.NoError(t, owner.Close())
	next, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	require.NoError(t, next.Close())
	_, err = os.Lstat(statePath)
	require.True(t, os.IsNotExist(err))
}

func TestManagerOpenStampsAfterStartupAndNeverRestampsRecovery(t *testing.T) {
	m, _, _ := recoveryManager(t)
	constructed := m.startedAt
	entered, release, gateDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		gateDone <- m.sshMaster.WithConnection(context.Background(), func(uint64, bool) error { close(entered); <-release; return nil })
	}()
	<-entered
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	opened := make(chan error, 1)
	go func() { opened <- m.Open(context.Background()) }()
	t.Cleanup(func() { unblock(); <-gateDone; _, err := m.Shutdown(); require.NoError(t, err) })
	require.Nil(t, m.stateWriter)
	require.Nil(t, m.socketServer)
	unblock()
	require.NoError(t, <-opened)
	require.True(t, m.startedAt.After(constructed))
	require.Equal(t, m.startedAt, m.metrics.startTime)
	started := m.startedAt
	for range 2 {
		require.NoError(t, m.sshMaster.Close())
		require.NoError(t, m.sshMaster.Open(context.Background()))
		require.Equal(t, started, m.startedAt)
		require.Equal(t, started, m.metrics.startTime)
	}
}

func TestConnectivityPipeProcess(t *testing.T) {
	if os.Getenv("S13_CONNECTIVITY_PIPE") == "" {
		return
	}
	if err := pipehold.Send(os.Getenv("S13_CONNECTIVITY_PIPE")); err != nil {
		os.Exit(41)
	}
	fmt.Println("27.0.0")
	os.Exit(0)
}

func TestConnectivityJoinsInheritedDescriptorWithinBudget(t *testing.T) {
	m, _, _ := recoveryManager(t)
	gate, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(os.Getenv("PATH"), "pipe-gate"), Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = gate.Close() })
	require.NoError(t, gate.SetDeadline(time.Now().Add(5*time.Second)))
	executable, err := os.Executable()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte(fmt.Sprintf("#!/bin/sh\nexec %q -test.run=^TestConnectivityPipeProcess$\n", executable)), 0700))
	t.Setenv("S13_CONNECTIVITY_PIPE", gate.Addr().String())
	result := make(chan error, 1)
	go func() { result <- m.validateDockerConnectivity(context.Background(), m.sshMaster.ControlPath()) }()
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
		t.Fatal("connectivity command did not join its copier while an inherited descriptor remained open")
	}
}

func TestShutdownRealBudgetRetainsNoncooperativeProducer(t *testing.T) {
	r := newWatchdogRun(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	// Registered after fixture cleanup: release precedes Run/child/owner joins.
	t.Cleanup(unblock)
	r.m.discovery.list = func(ctx context.Context) ([]string, error) { close(entered); <-release; return nil, ctx.Err() }
	r.start()
	<-entered
	r.awaitStream(t)
	r.m.state.SetDesired("held-owner", []int{18881})
	r.m.state.MarkActive("held-owner", 18881)
	created := r.m.state.GetByContainer("held-owner")[0].CreatedAt
	r.cancel()
	<-r.m.shutdown.ctx.Done()
	r.m.shutdown.mu.Lock()
	deadline := r.m.shutdown.deadline
	r.m.shutdown.mu.Unlock()
	time.Sleep(5 * time.Second)
	r.m.requestShutdown()
	r.m.shutdown.mu.Lock()
	require.Equal(t, deadline, r.m.shutdown.deadline, "repeat requests never renew the real budget")
	r.m.shutdown.mu.Unlock()
	time.Sleep(5100 * time.Millisecond)
	select {
	case <-r.done:
		t.Fatal("deadline abandoned a noncooperative producer")
	default:
	}
	require.Len(t, r.m.state.GetActual(), 1, "no terminal history consumption before producer quiescence")
	require.Empty(t, r.m.history.GetAll())
	contender, err := instancepath.Acquire(r.m.cfg.Host)
	if contender != nil {
		_ = contender.Close()
	}
	require.Error(t, err, "retain admission while noncooperative work is outstanding")
	unblock()
	<-r.done
	require.ErrorIs(t, r.err, context.DeadlineExceeded, "late successful cleanup remains exit1")
	history := r.m.history.GetAll()
	require.Len(t, history, 1)
	require.Equal(t, created, history[0].StartedAt)
	require.Equal(t, "rdhpf shutdown", history[0].EndReason)
	t.Logf("REAL_SHUTDOWN_BUDGET first-deadline=%s completed=%s sticky-failure=%v producer-joined=true", deadline, time.Now(), r.err)
}
