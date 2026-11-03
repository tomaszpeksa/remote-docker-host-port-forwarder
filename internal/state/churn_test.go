package state

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChurnRetiresEmptyDesired(t *testing.T) {
	for _, mode := range []string{"never-started", "finished", "retired", "invalidated", "inventory-absent", "inventory-portless"} {
		t.Run(mode, func(t *testing.T) {
			s := NewState()
			s.SetDesired("owner", []int{8080})
			if mode == "inventory-absent" {
				s.MergeInventory(nil, nil)
			} else if mode == "inventory-portless" {
				s.MergeInventory([]string{"owner"}, []ContainerPorts{{ContainerID: "owner"}})
			} else {
				if mode != "never-started" {
					s.MarkPending("owner", 8080, "uncertain")
					s.SetResource(ForwardResource{ContainerID: "owner", Port: 8080, Ownership: ForwardUnknown})
				}
				s.SetDesired("owner", nil)
				if mode != "never-started" {
					require.Len(t, s.GetDesired(), 1, "unknown actual/resource obligations retain empty owner")
					switch mode {
					case "finished":
						r, _ := s.Resource("owner", 8080)
						s.FinishResource(r)
					case "retired":
						s.RetireBefore(1, time.Now())
					case "invalidated":
						s.InvalidateConnection(time.Now())
					}
				}
			}
			require.Empty(t, s.GetDesired(), "empty desired owner must retire after its final obligation")
		})
	}
}

func TestChurnRetainsResourceOnlyOwner(t *testing.T) {
	s := NewState()
	r := ForwardResource{ContainerID: "owner", Port: 8080, Ownership: ForwardUnknown}
	s.SetResource(r)
	s.SetDesired("owner", nil)
	require.Len(t, s.GetDesired(), 1)
	s.MergeInventory(nil, nil)
	require.Len(t, s.GetDesired(), 1)
	s.FinishResource(r)
	require.Empty(t, s.GetDesired())
}
