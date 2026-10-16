package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Absence is authoritative, but a listed unknown owner must retain its latest value.
func TestMergeInventory(t *testing.T) {
	s := NewState()
	s.SetDesired("absent", []int{1001})
	s.SetDesired("unknown", []int{2002})
	s.SetActual("unknown", 2002, "active", "")
	actual := s.GetActual()
	observed := []ContainerPorts{{ContainerID: "known", Ports: []int{3003}}}
	require.True(t, s.MergeInventory([]string{"unknown", "known", "new-unknown"}, observed), "ABSENCE_AND_PARTIAL_PROGRESS")
	require.ElementsMatch(t, []ContainerPorts{{ContainerID: "absent", Ports: []int{}}, {ContainerID: "unknown", Ports: []int{2002}}, {ContainerID: "known", Ports: []int{3003}}}, s.GetDesired())
	observed[0].Ports[0] = 9999
	require.ElementsMatch(t, actual, s.GetActual(), "inventory never retires actual ownership")
	require.False(t, s.MergeInventory([]string{"unknown", "known"}, []ContainerPorts{{ContainerID: "known", Ports: []int{3003, 3003}}}), "port set equality, not order/duplicates")
	s.SetDesired("unknown", []int{2004})
	require.False(t, s.MergeInventory([]string{"unknown", "known"}, nil))
	require.Contains(t, s.GetDesired(), ContainerPorts{ContainerID: "unknown", Ports: []int{2004}}, "preserve latest, not a pass-start snapshot")
	require.True(t, s.MergeInventory(nil, nil), "complete empty list removes desired")
	require.Equal(t, actual, s.GetActual())
}

func TestMergeInventoryKnownEmptyDoesNotInventOwner(t *testing.T) {
	s := NewState()
	empty := []ContainerPorts{{ContainerID: "infrastructure", Ports: []int{}}}
	require.False(t, s.MergeInventory([]string{"infrastructure"}, empty), "NEW_EMPTY_INVENTORY_OWNER")
	require.Empty(t, s.GetDesired(), "startup previously omitted infrastructure and portless owners")
	s.SetDesired("infrastructure", []int{1001})
	require.True(t, s.MergeInventory([]string{"infrastructure"}, empty), "authoritative empty must still clear an existing owner")
	require.Contains(t, s.GetDesired(), ContainerPorts{ContainerID: "infrastructure", Ports: []int{}})
	require.False(t, s.MergeInventory([]string{"infrastructure"}, empty), "existing event/absence tombstone remains unchanged")
}
