package manager

import (
	"context"
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
