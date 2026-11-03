package state

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistoryExpiresOnIdleObservation(t *testing.T) {
	for _, observation := range []string{"GetAll", "Count"} {
		t.Run(observation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := NewHistory()
				ended := time.Now()
				h.Add(HistoryEntry{ContainerID: "original", Port: 8080, StartedAt: ended.Add(-time.Minute), EndedAt: ended, EndReason: "container stopped", FinalStatus: "active"})
				time.Sleep(time.Hour - time.Nanosecond)
				require.Equal(t, 1, h.Count())
				require.Equal(t, "container stopped", h.GetAll()[0].EndReason)
				time.Sleep(time.Nanosecond)
				if observation == "GetAll" {
					require.Empty(t, h.GetAll(), "exactly one hour expires even without insertion")
				} else {
					require.Zero(t, h.Count(), "count must expire idle entries too")
				}
			})
		})
	}
}

func TestHistoryInsertionLimitAndDefensiveCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := NewHistory()
		now := time.Now()
		h.Add(HistoryEntry{ContainerID: "expired", EndedAt: now.Add(-time.Hour)})
		require.Zero(t, h.Count())
		for i := 0; i < 101; i++ {
			h.Add(HistoryEntry{ContainerID: fmt.Sprintf("owner-%03d", i), StartedAt: now.Add(-time.Minute), EndedAt: now, EndReason: "original reason", FinalStatus: "conflict"})
		}
		require.Equal(t, 100, h.Count())
		entries := h.GetAll()
		require.Equal(t, "owner-001", entries[0].ContainerID)
		require.Equal(t, "owner-100", entries[99].ContainerID)
		entries[0].EndReason = "mutated"
		original := h.GetAll()[0]
		require.Equal(t, "original reason", original.EndReason)
		require.Equal(t, now.Add(-time.Minute), original.StartedAt)
		require.Equal(t, now, original.EndedAt)
		require.Equal(t, "conflict", original.FinalStatus)
		time.Sleep(time.Hour)
		h.Add(HistoryEntry{ContainerID: "new", EndedAt: time.Now()})
		require.Equal(t, []HistoryEntry{{ContainerID: "new", EndedAt: time.Now()}}, h.GetAll())
	})
}
