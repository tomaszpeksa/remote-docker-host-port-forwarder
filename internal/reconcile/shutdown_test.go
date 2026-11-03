package reconcile

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestOneHistoryEntryAfterQuiescentShutdown(t *testing.T) {
	st, history := state.NewState(), state.NewHistory()
	r := NewReconciler(st, history, slog.New(slog.NewTextHandler(io.Discard, nil)))
	at := time.Now()
	r.now = func() time.Time { return at }
	for _, tt := range []struct {
		owner, status, reason string
		ownership             state.ForwardOwnership
		port                  int
	}{
		{"active", "active", "", state.ForwardOwned, 8080},
		{"pending", "pending", "container stopped", state.ForwardOwned, 8081},
		{"lost-reply", "pending", "port claimed by other", state.ForwardUnknown, 8082},
		{"conflict", "conflict", "", state.ForwardUnowned, 8083},
	} {
		st.SetDesired(tt.owner, []int{tt.port})
		st.SetActual(tt.owner, tt.port, tt.status, "initial status")
		st.SetResource(state.ForwardResource{ContainerID: tt.owner, Port: tt.port, Ownership: tt.ownership, EndReason: tt.reason})
	}
	st.SetDesired("desired-only", []int{9090})
	st.SetResource(state.ForwardResource{ContainerID: "resource-only", Port: 9091, Ownership: state.ForwardUnknown})
	before := st.GetActual()
	at = time.Now()
	r.FinalizeShutdown() // caller supplies whole-Master proof and producer quiescence
	r.FinalizeShutdown()
	require.Empty(t, st.GetActual())
	require.Empty(t, st.GetDesired())
	require.Empty(t, st.GetResources())
	entries := history.GetAll()
	require.Len(t, entries, 4, "no desired/resource-only fabrication or repeat consumption")
	for _, forward := range before {
		var found []state.HistoryEntry
		for _, entry := range entries {
			if entry.ContainerID == forward.ContainerID {
				found = append(found, entry)
			}
		}
		require.Len(t, found, 1)
		require.Equal(t, forward.CreatedAt, found[0].StartedAt)
		require.Equal(t, at, found[0].EndedAt)
		require.Equal(t, forward.Status, found[0].FinalStatus)
		switch forward.ContainerID {
		case "pending":
			require.Equal(t, "container stopped", found[0].EndReason)
		case "lost-reply":
			require.Equal(t, "port claimed by other", found[0].EndReason)
		default:
			require.Equal(t, "rdhpf shutdown", found[0].EndReason)
		}
	}
}
