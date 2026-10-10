package state

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInvalidateConnectionPreservesLogicalForwards(t *testing.T) {
	s := NewState()
	s.SetDesired("desired", []int{1, 2, 3})
	s.MarkActive("desired", 1)
	s.MarkPending("desired", 2, "probe failed")
	s.MarkConflict("desired", 3, "occupied")
	s.MarkActive("stopped", 4)
	s.MarkPending("stopped", 5, "pending")
	s.MarkConflict("stopped", 6, "conflict")
	before := s.GetByContainer("desired")
	at := time.Now().Add(time.Second)
	retired := s.InvalidateConnection(at)
	require.Len(t, retired, 3)
	require.Empty(t, s.GetByContainer("stopped"))
	require.ElementsMatch(t, []ContainerPorts{{ContainerID: "desired", Ports: []int{1, 2, 3}}}, s.GetDesired())
	for _, previous := range before {
		var actual ForwardState
		for _, fs := range s.GetByContainer("desired") {
			if fs.Port == previous.Port {
				actual = fs
			}
		}
		require.Equal(t, previous.CreatedAt, actual.CreatedAt)
		if actual.Port == 1 {
			require.Equal(t, "pending", actual.Status)
			require.Equal(t, "SSH connection lost", actual.Reason)
			require.Equal(t, at, actual.UpdatedAt)
		} else {
			require.Equal(t, previous, actual)
		}
	}
	require.Empty(t, s.InvalidateConnection(at.Add(time.Second)), "no duplicate stop-history candidates")
}
