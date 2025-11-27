package unit

import (
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// TestReconciler_Diff_Idempotent verifies that calling Diff() multiple times
// with the same state returns the same actions each time
func TestReconciler_Diff_Idempotent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Setup: Container wants port 8080, none exist yet
	st.SetDesired("container1", []int{8080})

	// First diff
	toAdd1, toRemove1 := reconciler.Diff()

	// Second diff (without changing state)
	toAdd2, toRemove2 := reconciler.Diff()

	// Third diff (without changing state)
	toAdd3, toRemove3 := reconciler.Diff()

	// All diffs should be identical
	assert.Equal(t, len(toAdd1), len(toAdd2), "First and second diff should have same number of add actions")
	assert.Equal(t, len(toAdd1), len(toAdd3), "First and third diff should have same number of add actions")
	assert.Equal(t, len(toRemove1), len(toRemove2), "First and second diff should have same number of remove actions")
	assert.Equal(t, len(toRemove1), len(toRemove3), "First and third diff should have same number of remove actions")

	// Check specific actions
	assert.Len(t, toAdd1, 1, "Should have exactly one add action")
	assert.Len(t, toRemove1, 0, "Should have no remove actions")

	assert.Equal(t, "add", toAdd1[0].Type)
	assert.Equal(t, "container1", toAdd1[0].ContainerID)
	assert.Equal(t, 8080, toAdd1[0].Port)
}

// TestReconciler_Diff_NoOpWhenEqual verifies no actions when desired equals actual
func TestReconciler_Diff_NoOpWhenEqual(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Setup: Container wants port 8080 and it's already active
	st.SetDesired("container1", []int{8080})
	st.MarkActive("container1", 8080)

	// Diff should show no actions needed
	toAdd, toRemove := reconciler.Diff()

	assert.Len(t, toAdd, 0, "Should have no add actions when state matches")
	assert.Len(t, toRemove, 0, "Should have no remove actions when state matches")
}

// TestReconciler_Diff_MultipleContainersSameState verifies no actions for multiple
// containers when all are in sync
func TestReconciler_Diff_NoOpMultipleContainers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Setup: Two containers, each with their ports already active
	st.SetDesired("container1", []int{8080, 8081})
	st.MarkActive("container1", 8080)
	st.MarkActive("container1", 8081)

	st.SetDesired("container2", []int{9090})
	st.MarkActive("container2", 9090)

	// Diff should show no actions
	toAdd, toRemove := reconciler.Diff()

	assert.Len(t, toAdd, 0, "Should have no add actions")
	assert.Len(t, toRemove, 0, "Should have no remove actions")
}

// TestReconciler_Apply_Idempotent verifies that Apply() is idempotent
// NOTE: This test cannot fully test Apply() without a real SSH connection,
// but we can verify the reconciler's decision-making is idempotent
func TestReconciler_Apply_IdempotentDecisions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Setup: Container wants port 8080
	st.SetDesired("container1", []int{8080})

	// First reconciliation cycle
	toAdd1, _ := reconciler.Diff()
	assert.Len(t, toAdd1, 1, "First diff should want to add port 8080")

	// Simulate successful apply by marking port active
	st.MarkActive("container1", 8080)

	// Second reconciliation cycle - should be no-op
	toAdd2, toRemove2 := reconciler.Diff()
	assert.Len(t, toAdd2, 0, "Second diff should have no add actions")
	assert.Len(t, toRemove2, 0, "Second diff should have no remove actions")

	// Third reconciliation cycle - should still be no-op
	toAdd3, toRemove3 := reconciler.Diff()
	assert.Len(t, toAdd3, 0, "Third diff should have no add actions")
	assert.Len(t, toRemove3, 0, "Third diff should have no remove actions")
}

// TestReconciler_Diff_SkipsAlreadyCompleted verifies that Diff() skips
// actions for forwards that already exist
func TestReconciler_Diff_SkipsAlreadyCompleted(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Container wants ports 8080 and 808, but 8080 is already active
	st.SetDesired("container1", []int{8080, 8081})
	st.MarkActive("container1", 8080)

	toAdd, toRemove := reconciler.Diff()

	// Should only want to add 8081, not 8080
	assert.Len(t, toAdd, 1, "Should only add the missing port")
	assert.Equal(t, 8081, toAdd[0].Port, "Should add port 8081")
	assert.Len(t, toRemove, 0, "Should have no remove actions")
}

// TestReconciler_Diff_SkipsRemovalIfAlreadyGone verifies that Diff() doesn't
// try to remove forwards that are already gone
func TestReconciler_Diff_SkipsRemovalIfAlreadyGone(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Container initially had port 8080, but now wants nothing
	// However, actual state shows no active forwards (already cleaned)
	st.SetDesired("container1", []int{})

	toAdd, toRemove := reconciler.Diff()

	// Should have no actions since forward is already gone
	assert.Len(t, toAdd, 0, "Should have no add actions")
	assert.Len(t, toRemove, 0, "Should have no remove actions")
}

// TestReconciler_Diff_OnlyCountsActiveForwards verifies that only "active"
// forwards are considered in actual state, not "pending" or "conflict"
func TestReconciler_Diff_OnlyCountsActiveForwards(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Container wants port 8080
	st.SetDesired("container1", []int{8080})

	// But port is in "pending" state (not active)
	st.MarkPending("container1", 8080, "waiting")

	toAdd, _ := reconciler.Diff()

	// Should still try to add since it's not "active"
	assert.Len(t, toAdd, 1, "Should try to add forward even though it exists as pending")
	assert.Equal(t, 8080, toAdd[0].Port)

	// Now mark as conflict
	st.MarkConflict("container1", 8080, "port in use")
	toAdd, _ = reconciler.Diff()

	// Should still try to add since it's not "active"
	assert.Len(t, toAdd, 1, "Should try to add forward even though it exists as conflict")
}

// T036: Unit test - Batch operations for multiple containers

// TestReconciler_Diff_BatchAddMultipleContainers verifies adding 2 containers
// with multiple ports each
func TestReconciler_Diff_BatchAddMultipleContainers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Add 2 containers with multiple ports each
	st.SetDesired("container1", []int{8080, 8081, 8082})
	st.SetDesired("container2", []int{9090, 9091})

	toAdd, toRemove := reconciler.Diff()

	// Should have 5 total add actions (3 + 2)
	assert.Len(t, toAdd, 5, "Should have 5 add actions total")
	assert.Len(t, toRemove, 0, "Should have no remove actions")

	// Verify all ports are accounted for
	container1Ports := make(map[int]bool)
	container2Ports := make(map[int]bool)

	for _, action := range toAdd {
		assert.Equal(t, "add", action.Type)
		if action.ContainerID == "container1" {
			container1Ports[action.Port] = true
		} else if action.ContainerID == "container2" {
			container2Ports[action.Port] = true
		} else {
			t.Errorf("Unexpected container ID: %s", action.ContainerID)
		}
	}

	// Verify container1 has all its ports
	assert.True(t, container1Ports[8080], "Container1 should have port 8080")
	assert.True(t, container1Ports[8081], "Container1 should have port 8081")
	assert.True(t, container1Ports[8082], "Container1 should have port 8082")

	// Verify container2 has all its ports
	assert.True(t, container2Ports[9090], "Container2 should have port 9090")
	assert.True(t, container2Ports[9091], "Container2 should have port 9091")
}

// TestReconciler_Diff_RemoveOneKeepAnother verifies removing 1 container
// while keeping another
func TestReconciler_Diff_RemoveOneKeepAnother(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Setup: Two containers with active forwards
	st.SetDesired("container1", []int{8080, 8081})
	st.MarkActive("container1", 8080)
	st.MarkActive("container1", 8081)

	st.SetDesired("container2", []int{9090})
	st.MarkActive("container2", 9090)

	// Now remove container1 (set desired to empty)
	st.SetDesired("container1", []int{})

	toAdd, toRemove := reconciler.Diff()

	// Should only remove container1's ports, not container2
	assert.Len(t, toAdd, 0, "Should have no add actions")
	assert.Len(t, toRemove, 2, "Should remove 2 ports from container1")

	// Verify we're only removing container1's ports
	for _, action := range toRemove {
		assert.Equal(t, "remove", action.Type)
		assert.Equal(t, "container1", action.ContainerID, "Should only remove container1's ports")
		assert.Contains(t, []int{8080, 8081}, action.Port, "Should remove container1's ports")
	}
}

// TestReconciler_Diff_OperationsGroupedByContainer verifies that operations
// are logically grouped by container
func TestReconciler_Diff_OperationsGroupedByContainer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Container1: add 2 ports
	st.SetDesired("container1", []int{8080, 8081})

	// Container2: add 1 port, remove 1 port
	st.SetDesired("container2", []int{9090})
	st.MarkActive("container2", 9091) // This port is no longer desired

	// Container3: remove all ports
	st.SetDesired("container3", []int{})
	st.MarkActive("container3", 7070)
	st.MarkActive("container3", 7071)

	toAdd, toRemove := reconciler.Diff()

	// Count actions by container
	addByContainer := make(map[string]int)
	removeByContainer := make(map[string]int)

	for _, action := range toAdd {
		addByContainer[action.ContainerID]++
	}
	for _, action := range toRemove {
		removeByContainer[action.ContainerID]++
	}

	// Verify operations are properly grouped
	assert.Equal(t, 2, addByContainer["container1"], "Container1 should have 2 add actions")
	assert.Equal(t, 1, addByContainer["container2"], "Container2 should have 1 add action")
	assert.Equal(t, 0, addByContainer["container3"], "Container3 should have 0 add actions")

	assert.Equal(t, 0, removeByContainer["container1"], "Container1 should have 0 remove actions")
	assert.Equal(t, 1, removeByContainer["container2"], "Container2 should have 1 remove action")
	assert.Equal(t, 2, removeByContainer["container3"], "Container3 should have 2 remove actions")
}

// TestReconciler_Diff_MixedOperationsMultipleContainers verifies complex
// scenario with adds and removes across multiple containers
func TestReconciler_Diff_MixedOperationsMultipleContainers(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	reconciler := reconcile.NewReconciler(st, state.NewHistory(), logger)

	// Container1: has 8080, wants 8080 and 8081 (add 8081)
	st.SetDesired("container1", []int{8080, 8081})
	st.MarkActive("container1", 8080)

	// Container2: has 9090, wants nothing (remove 9090)
	st.SetDesired("container2", []int{})
	st.MarkActive("container2", 9090)

	// Container3: has nothing, wants 7070 (add 7070)
	st.SetDesired("container3", []int{7070})

	toAdd, toRemove := reconciler.Diff()

	assert.Len(t, toAdd, 2, "Should have 2 add actions (8081, 7070)")
	assert.Len(t, toRemove, 1, "Should have 1 remove action (9090)")

	// Verify specific actions
	addPorts := make(map[string][]int)
	for _, action := range toAdd {
		addPorts[action.ContainerID] = append(addPorts[action.ContainerID], action.Port)
	}

	assert.Contains(t, addPorts["container1"], 8081)
	assert.Contains(t, addPorts["container3"], 7070)

	assert.Equal(t, "remove", toRemove[0].Type)
	assert.Equal(t, "container2", toRemove[0].ContainerID)
	assert.Equal(t, 9090, toRemove[0].Port)
}
