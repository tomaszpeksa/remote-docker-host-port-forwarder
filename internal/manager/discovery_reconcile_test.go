package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestManagerDiscoveryUnappliedEOF(t *testing.T) {
	m, port, calls := recoveryManager(t)
	m.state.SetDesired(discoveryA, []int{port})
	m.state.MarkActive(discoveryA, port)
	require.NoError(t, m.deliverEvent(context.Background(), 0, docker.Event{Type: "stop", ContainerID: discoveryA}))
	terminal := make(chan docker.StreamResult, 1)
	terminal <- docker.StreamResult{}
	close(terminal)
	require.True(t, m.runEventLoop(context.Background(), terminal, 0))
	require.Equal(t, uint64(1), m.discovery.request, "EOF_MUST_RETAIN_UNAPPLIED_REQUEST")
	require.Len(t, m.discovery.reconcileWake, 1, "EOF_MUST_WAKE_BEFORE_DEBOUNCE")
	require.Error(t, m.reconcileConnection(context.Background(), 0, false))
	require.Zero(t, m.discovery.attempted, "UNAVAILABLE_IS_NOT_AN_ATTEMPT")
	require.NoError(t, m.reconcileConnection(context.Background(), 0, true))
	require.Equal(t, m.discovery.request, m.discovery.attempted)
	require.Empty(t, m.state.GetByContainer(discoveryA))
	require.Len(t, m.history.GetAll(), 1)
	require.Equal(t, "container stopped", m.history.GetAll()[0].EndReason)
	b, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Contains(t, string(b), "-O cancel")
}

func TestManagerDiscoveryBufferedTerminal(t *testing.T) {
	for _, nonzero := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "Exit7"}[nonzero], func(t *testing.T) {
			m, port, calls := recoveryManager(t)
			m.state.SetDesired(discoveryB, []int{port})
			m.state.MarkActive(discoveryB, port)
			created := m.state.GetByContainer(discoveryB)[0].CreatedAt
			dir := filepath.Dir(calls)
			entered, release := filepath.Join(dir, "inspect-entered"), filepath.Join(dir, "inspect-release")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\ncase \"$*\" in *HostConfig.PortBindings*) printf entered > %q; while [ ! -f %q ]; do :; done; printf '{}';; esac\n", calls, entered, release)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan bool, 1)
			joined := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				_ = os.WriteFile(release, nil, 0600)
				<-joined
			})
			require.NoError(t, m.deliverEvent(ctx, 0, docker.Event{Type: "start", ContainerID: discoveryA}))
			require.NoError(t, m.deliverEvent(ctx, 0, docker.Event{Type: "stop", ContainerID: discoveryB}))
			terminal := make(chan docker.StreamResult, 1)
			result := docker.StreamResult{}
			if nonzero {
				result.Err = errors.New("exit status 7")
			}
			terminal <- result
			close(terminal)
			go func() { defer close(joined); done <- m.runEventLoop(ctx, terminal, 0) }()
			require.Eventually(t, func() bool { _, err := os.Stat(entered); return err == nil }, time.Second, time.Millisecond, "BUFFERED_TERMINAL_MUST_DRAIN")
			require.Equal(t, 2, pendingWatchdogEvents(m), "BUFFERED_TERMINAL_MUST_RETAIN_ACCEPTED")
			require.Error(t, m.finishStream(0), "pending work must forbid replacement")
			select {
			case <-done:
				t.Fatal("BUFFERED_TERMINAL_MUST_DRAIN")
			default:
			}
			require.NoError(t, os.WriteFile(release, nil, 0600))
			require.Equal(t, !nonzero, <-done)
			<-joined
			require.Zero(t, pendingWatchdogEvents(m), "BUFFERED_TERMINAL_MUST_RETIRE")
			require.EqualValues(t, 2, m.discovery.request, "BUFFERED_TERMINAL_MUST_DRAIN")
			require.NoError(t, m.finishStream(0))
			require.EqualValues(t, 1, m.discovery.generation)
			committed, _, err := m.commitInventory(inventoryCandidate{listedComplete: true})
			require.NoError(t, err)
			require.False(t, committed, "old inventory generation cannot overwrite drained events")
			require.Len(t, m.discovery.reconcileWake, 1)
			require.Zero(t, m.discovery.attempted, "draining is not a reconciliation acknowledgment")
			require.NoError(t, m.reconcileConnection(ctx, 0, true))
			require.Empty(t, m.state.GetByContainer(discoveryB))
			require.Len(t, m.history.GetAll(), 1)
			require.Equal(t, created, m.history.GetAll()[0].StartedAt)
		})
	}
}

func TestManagerDiscoveryApplyFailureDischarged(t *testing.T) {
	m, port, calls := recoveryManager(t)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(calls), "ssh"), []byte("#!/bin/sh\nexit 1\n"), 0700))
	m.state.SetDesired(discoveryA, []int{port})
	m.discovery.request = 1
	require.Error(t, m.reconcileConnection(context.Background(), 0, true))
	require.Equal(t, uint64(1), m.discovery.attempted, "REAL_FAILED_APPLY_DISCHARGES_CAPTURED_REQUEST")
	require.Equal(t, "conflict", m.state.GetByContainer(discoveryA)[0].Status)
	// A complete unchanged audit must not create an operation-retry obligation.
	candidate := inventoryCandidate{listedComplete: true, listed: []string{discoveryA}, observed: []state.ContainerPorts{{ContainerID: discoveryA, Ports: []int{port}}}}
	committed, changed, err := m.commitInventory(candidate)
	require.NoError(t, err)
	require.True(t, committed)
	require.False(t, changed)
	require.Equal(t, m.discovery.request, m.discovery.attempted, "NO_PERPETUAL_OPERATION_RETRY")
}

func TestManagerDiscoveryNewerRequestSurvivesApply(t *testing.T) {
	m, port, calls := recoveryManager(t)
	dir := filepath.Dir(calls)
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(fmt.Sprintf("#!/bin/sh\nprintf ready > %q\nwhile [ ! -f %q ]; do :; done\nexit 0\n", started, release)), 0700))
	m.state.SetDesired(discoveryA, []int{port})
	m.discovery.request = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.reconcileConnection(ctx, 0, true) }()
	require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil }, time.Second, time.Millisecond)
	require.NoError(t, m.setEventDesired(ctx, 0, discoveryA, []int{port + 1}))
	require.NoError(t, os.WriteFile(release, nil, 0600))
	require.NoError(t, <-done)
	require.Equal(t, uint64(2), m.discovery.request, "NEWER_DESIRED_REQUEST")
	require.Equal(t, uint64(1), m.discovery.attempted, "ONLY_CAPTURED_REVISION_ACKNOWLEDGED")
	add, remove := m.reconciler.Diff()
	require.Len(t, add, 1)
	require.Len(t, remove, 1)
	require.Equal(t, port+1, add[0].Port)
}
