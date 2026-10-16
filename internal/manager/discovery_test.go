package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
)

// Without the event inspection budget a silent SSH child consumes the caller's
// longer deadline. The outer context is only a safety release, not the oracle.
func TestDiscoveryObservationEventInspectionDeadline(t *testing.T) {
	m, port, _ := recoveryManager(t)
	m.state.SetDesired(identityContainerID, []int{port})
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\nwhile :; do :; done\n"), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	started := time.Now()
	err := m.handleStartEvent(ctx, docker.Event{Type: "start", ContainerID: identityContainerID}, 0)
	require.Less(t, time.Since(started), 6*time.Second, "EVENT_INSPECTION_HAS_OWN_BUDGET")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, ctx.Err(), "caller safety deadline must not release production")
	require.Equal(t, []int{port}, m.state.GetDesired()[0].Ports)
}

func TestDiscoveryObservationInfrastructureError(t *testing.T) {
	m, _, _ := recoveryManager(t)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\nprintf 'inspect failed' >&2\nexit 7\n"), 0700))
	_, err := m.hasTestInfrastructureLabel(context.Background(), m.sshMaster.ControlPath(), identityContainerID)
	require.Error(t, err, "INFRASTRUCTURE_FAILURE_IS_UNKNOWN")
}

func TestDiscoveryObservationList(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		wantError    bool
	}{
		{"empty", "exit 0", false},
		{"failed-prefix", "printf '0123456789abcdef\\n'; exit 7", true},
		{"malformed", "printf 'short\\n'", true},
		{"oversized", "i=0; while [ \"$i\" -lt 4200 ]; do printf '%1000s' ''; i=$((i+1)); done", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := recoveryManager(t)
			require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0700))
			ids, err := m.listRunning(context.Background())
			if tc.wantError {
				require.Error(t, err, "LIST_MUST_BE_COMPLETE")
				require.Nil(t, ids)
			} else {
				require.NoError(t, err)
				require.Empty(t, ids)
			}
		})
	}
}

// Existing command/identity tests exercise the worker's production pass/merge,
// without manufacturing inventory data or claiming native Manager.Run proof.
func startupInventoryForTest(m *Manager, ctx context.Context) error {
	candidate := m.inventoryPass(ctx, inventoryFence{}, "")
	if candidate.problem != nil {
		return candidate.problem
	}
	_, _, err := m.commitInventory(candidate)
	return err
}
