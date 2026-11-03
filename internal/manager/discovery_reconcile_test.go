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
	ctx := context.Background()
	m.state.SetDesired(discoveryA, []int{port})
	m.discovery.request = 1
	require.Error(t, m.triggerReconcile(ctx))
	require.Zero(t, m.discovery.attempted, "UNAVAILABLE_IS_NOT_AN_ATTEMPT")
	require.NoError(t, m.sshMaster.Open(ctx))
	require.NoError(t, m.triggerReconcile(ctx))
	require.NoError(t, m.deliverEvent(ctx, 0, docker.Event{Type: "stop", ContainerID: discoveryA}))
	terminal := make(chan docker.StreamResult, 1)
	terminal <- docker.StreamResult{}
	close(terminal)
	require.True(t, m.runEventLoop(ctx, terminal, 0))
	require.EqualValues(t, 2, m.discovery.request, "EOF_MUST_RETAIN_UNAPPLIED_REQUEST")
	require.Len(t, m.discovery.reconcileWake, 1, "EOF_MUST_WAKE_BEFORE_DEBOUNCE")
	require.NoError(t, m.triggerReconcile(ctx))
	require.Equal(t, m.discovery.request, m.discovery.attempted)
	require.Empty(t, m.state.GetByContainer(discoveryA))
	require.Len(t, m.history.GetAll(), 1)
	require.Equal(t, "container stopped", m.history.GetAll()[0].EndReason)
	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Contains(t, string(data), "CLOSE")
}

func TestManagerDiscoveryBufferedTerminal(t *testing.T) {
	for _, nonzero := range []bool{false, true} {
		t.Run(map[bool]string{false: "EOF", true: "Exit7"}[nonzero], func(t *testing.T) {
			m, port, calls := recoveryManager(t)
			m.state.SetDesired(discoveryB, []int{port})
			m.discovery.request = 1
			require.NoError(t, m.sshMaster.Open(context.Background()))
			require.NoError(t, m.triggerReconcile(context.Background()))
			created := m.state.GetByContainer(discoveryB)[0].CreatedAt
			dir := filepath.Dir(calls)
			entered, release := filepath.Join(dir, "inspect-entered"), filepath.Join(dir, "inspect-release")
			original, err := os.ReadFile(filepath.Join(dir, "ssh"))
			require.NoError(t, err)
			script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *HostConfig.PortBindings*) printf entered > %q; while [ ! -f %q ]; do :; done; printf '{}';exit 0;; esac\n", entered, release)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), append([]byte(script), original...), 0700))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan bool, 1)
			joined := make(chan struct{})
			t.Cleanup(func() { cancel(); _ = os.WriteFile(release, nil, 0600); <-joined })
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
			require.EqualValues(t, 3, m.discovery.request, "BUFFERED_TERMINAL_MUST_DRAIN")
			require.NoError(t, m.finishStream(0))
			require.EqualValues(t, 1, m.discovery.generation)
			committed, _, err := m.commitInventory(inventoryCandidate{listedComplete: true})
			require.NoError(t, err)
			require.False(t, committed, "old inventory generation cannot overwrite drained events")
			require.Len(t, m.discovery.reconcileWake, 1)
			require.EqualValues(t, 1, m.discovery.attempted, "draining is not a reconciliation acknowledgment")
			require.NoError(t, m.triggerReconcile(ctx))
			require.Empty(t, m.state.GetByContainer(discoveryB))
			require.Len(t, m.history.GetAll(), 1)
			require.Equal(t, created, m.history.GetAll()[0].StartedAt)
		})
	}
}

func TestManagerDiscoveryApplyFailureDischarged(t *testing.T) {
	m, port, _ := recoveryManager(t)
	require.NoError(t, os.WriteFile(os.Getenv("S12_MUX_OPEN_FAIL"), []byte("injected rejection"), 0600))
	require.NoError(t, m.sshMaster.Open(context.Background()))
	m.state.SetDesired(discoveryA, []int{port})
	m.discovery.request = 1
	require.Error(t, m.triggerReconcile(context.Background()))
	require.EqualValues(t, 1, m.discovery.attempted, "REAL_FAILED_APPLY_DISCHARGES_CAPTURED_REQUEST")
	require.Equal(t, "conflict", m.state.GetByContainer(discoveryA)[0].Status)
	candidate := inventoryCandidate{listedComplete: true, listed: []string{discoveryA}, observed: []state.ContainerPorts{{ContainerID: discoveryA, Ports: []int{port}}}}
	committed, changed, err := m.commitInventory(candidate)
	require.NoError(t, err)
	require.True(t, committed)
	require.False(t, changed)
	require.Equal(t, m.discovery.request, m.discovery.attempted, "UNCHANGED_AUDIT_DOES_NOT_REISSUE_DISCOVERY_REQUEST")
	_, pending := m.reconciler.NextRetry()
	require.True(t, pending, "resource retry is independent of discharged discovery request")
}

func TestManagerDiscoveryNewerRequestSurvivesApply(t *testing.T) {
	m, port, calls := recoveryManager(t)
	gate := filepath.Join(filepath.Dir(calls), "open-gate")
	t.Setenv("S12_MUX_OPEN_GATE", gate)
	require.NoError(t, os.WriteFile(gate+".armed", nil, 0600))
	require.NoError(t, m.sshMaster.Open(context.Background()))
	m.state.SetDesired(discoveryA, []int{port})
	m.discovery.request = 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	joined := make(chan struct{})
	t.Cleanup(func() { cancel(); _ = os.WriteFile(gate+".release", nil, 0600); <-joined })
	go func() { defer close(joined); done <- m.triggerReconcile(ctx) }()
	require.Eventually(t, func() bool { _, err := os.Stat(gate + ".entered"); return err == nil }, time.Second, time.Millisecond)
	require.NoError(t, m.setEventDesired(ctx, 0, discoveryA, []int{port + 1}))
	require.NoError(t, os.WriteFile(gate+".release", nil, 0600))
	require.NoError(t, <-done)
	<-joined
	require.EqualValues(t, 2, m.discovery.request, "NEWER_DESIRED_REQUEST")
	require.EqualValues(t, 1, m.discovery.attempted, "ONLY_CAPTURED_REVISION_ACKNOWLEDGED")
	add, remove := m.reconciler.Diff()
	require.Len(t, add, 1)
	require.Len(t, remove, 1)
	require.Equal(t, port+1, add[0].Port)
}
