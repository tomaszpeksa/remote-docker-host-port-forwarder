package integration

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// T046: Integration test - Occupied port scenario
// TestConflict_OccupiedPort verifies that when a local port is already in use,
// the tool detects the conflict, logs it clearly, and other ports still work.
func TestConflict_OccupiedPort(t *testing.T) {
	sshHost := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, logger)

	// Setup SSH ControlMaster
	master, err := ssh.NewMaster(sshHost, logger)
	require.NoError(t, err, "Should create SSH master")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = master.Open(ctx)
	require.NoError(t, err, "Should open SSH ControlMaster")
	defer master.Close()

	// STEP 1: Occupy port 16379 locally (using high port to avoid conflicts)
	listener, err := net.Listen("tcp", "127.0.0.1:16379")
	require.NoError(t, err, "Should bind to localhost:16379")
	t.Logf("Occupied localhost:16379")

	// STEP 2: Try to forward conflicting port (16379) and non-conflicting port (8080)
	// Simulate container wanting both ports
	containerID := "test-container-conflict"
	st.SetDesired(containerID, []int{16379, 8080})

	toAdd, toRemove := reconciler.Diff()
	require.Len(t, toAdd, 2, "Should want to add 2 ports")
	require.Len(t, toRemove, 0, "Should have no removes")

	// Combine actions for Apply
	actions := append(toRemove, toAdd...)

	// STEP 3: Apply actions - expect 6379 to fail, 8080 to succeed
	err = reconciler.Apply(ctx, master, sshHost, actions)
	// We expect an error because one port failed
	assert.Error(t, err, "Apply should return error due to conflict on port 6379")
	assert.Contains(t, err.Error(), "reconciliation completed with errors",
		"Error should indicate some operations failed")

	// STEP 4: Verify state
	actualState := st.GetByContainer(containerID)

	var port16379State, port8080State *state.ForwardState
	for i := range actualState {
		if actualState[i].Port == 16379 {
			port16379State = &actualState[i]
		}
		if actualState[i].Port == 8080 {
			port8080State = &actualState[i]
		}
	}

	// Port 16379 should be marked as conflict
	require.NotNil(t, port16379State, "Port 16379 should exist in state")
	assert.Equal(t, "conflict", port16379State.Status,
		"Port 16379 should be marked as conflict")
	// SSH returns "Port forwarding failed" when port is occupied (remote error)
	assert.Contains(t, port16379State.Reason, "Port forwarding failed",
		"Conflict reason should mention port forwarding failure")

	// Port 8080 should be active (not affected by 16379 conflict)
	require.NotNil(t, port8080State, "Port 8080 should exist in state")
	assert.Equal(t, "active", port8080State.Status,
		"Port 8080 should be active despite 16379 conflict")

	t.Logf("✓ Port 16379: %s (%s)", port16379State.Status, port16379State.Reason)
	t.Logf("✓ Port 8080: %s", port8080State.Status)

	// Cleanup
	listener.Close()

	// Cancel the forwards
	st.SetDesired(containerID, []int{})
	toAdd, toRemove = reconciler.Diff()
	actions = append(toRemove, toAdd...)
	_ = reconciler.Apply(ctx, master, sshHost, actions)
}

// T047: Integration test - Port released scenario
// TestConflict_PortReleased verifies that when a conflicting port is released,
// the retry mechanism eventually succeeds in establishing the forward.
func TestConflict_PortReleased(t *testing.T) {
	sshHost := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, logger)

	// Setup SSH ControlMaster
	master, err := ssh.NewMaster(sshHost, logger)
	require.NoError(t, err, "Should create SSH master")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	err = master.Open(ctx)
	require.NoError(t, err, "Should open SSH ControlMaster")
	defer master.Close()

	// STEP 1: Occupy port 17070 locally (using high port to avoid conflicts)
	listener, err := net.Listen("tcp", "127.0.0.1:17070")
	require.NoError(t, err, "Should bind to localhost:17070")
	t.Logf("Occupied localhost:17070")

	// STEP 2: Try to forward the conflicting port
	containerID := "test-container-release"
	st.SetDesired(containerID, []int{17070})

	toAdd, toRemove := reconciler.Diff()
	require.Len(t, toAdd, 1, "Should want to add port 17070")

	actions := append(toRemove, toAdd...)

	// First attempt should fail
	err = reconciler.Apply(ctx, master, sshHost, actions)
	assert.Error(t, err, "First apply should fail due to conflict")

	// Verify conflict state
	actualState := st.GetByContainer(containerID)
	require.Len(t, actualState, 1, "Should have one forward state")
	assert.Equal(t, "conflict", actualState[0].Status,
		"Port should be in conflict state")
	t.Logf("Initial state: Port 17070 is %s", actualState[0].Status)

	// STEP 3: Release the port
	err = listener.Close()
	require.NoError(t, err, "Should close listener")
	t.Logf("Released localhost:17070")

	// Wait a moment for port to be fully released
	time.Sleep(100 * time.Millisecond)

	// STEP 4: Retry - should succeed now
	// Re-compute diff (port is still desired but in conflict state)
	toAdd, toRemove = reconciler.Diff()
	// Since status is "conflict" not "active", Diff should want to add it again
	require.Len(t, toAdd, 1, "Should want to retry adding port 17070")

	actions = append(toRemove, toAdd...)
	err = reconciler.Apply(ctx, master, sshHost, actions)
	assert.NoError(t, err, "Second apply should succeed after port released")

	// STEP 5: Verify port is now active
	actualState = st.GetByContainer(containerID)
	require.Len(t, actualState, 1, "Should have one forward state")
	assert.Equal(t, "active", actualState[0].Status,
		"Port should be active after release")
	t.Logf("After retry: Port 17070 is %s", actualState[0].Status)

	// Cleanup
	st.SetDesired(containerID, []int{})
	toAdd, toRemove = reconciler.Diff()
	actions = append(toRemove, toAdd...)
	_ = reconciler.Apply(ctx, master, sshHost, actions)
}

// TestConflict_MultipleContainersOneConflict verifies that a conflict in one
// container doesn't affect forwards for other containers
func TestConflict_MultipleContainersOneConflict(t *testing.T) {
	sshHost := getTestSSHHost(t)

	logger := logging.NewLogger("debug")
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, logger)

	master, err := ssh.NewMaster(sshHost, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	err = master.Open(ctx)
	require.NoError(t, err)
	defer master.Close()

	// Occupy port 19090 (using high port to avoid conflicts)
	listener, err := net.Listen("tcp", "127.0.0.1:19090")
	require.NoError(t, err)
	defer listener.Close()

	// Container 1: wants conflicting port 19090
	container1 := "container1"
	st.SetDesired(container1, []int{19090})

	// Container 2: wants non-conflicting port 19091
	container2 := "container2"
	st.SetDesired(container2, []int{19091})

	// Apply for both containers
	toAdd, toRemove := reconciler.Diff()
	actions := append(toRemove, toAdd...)

	err = reconciler.Apply(ctx, master, sshHost, actions)
	// Expect error due to container1's conflict
	assert.Error(t, err, "Should have error from container1")

	// Verify container1 has conflict
	state1 := st.GetByContainer(container1)
	require.Len(t, state1, 1)
	assert.Equal(t, "conflict", state1[0].Status,
		"Container1 port should be in conflict")

	// Verify container2 is active (not affected)
	state2 := st.GetByContainer(container2)
	require.Len(t, state2, 1)
	assert.Equal(t, "active", state2[0].Status,
		"Container2 port should be active")

	t.Logf("✓ Container1 (port 19090): %s - isolated from Container2",
		state1[0].Status)
	t.Logf("✓ Container2 (port 19091): %s - not affected by Container1 conflict",
		state2[0].Status)

	// Cleanup
	st.SetDesired(container1, []int{})
	st.SetDesired(container2, []int{})
	toAdd, toRemove = reconciler.Diff()
	actions = append(toRemove, toAdd...)
	_ = reconciler.Apply(ctx, master, sshHost, actions)
}
