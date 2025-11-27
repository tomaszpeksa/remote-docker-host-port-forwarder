package unit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestHistoryAdd_BasicFunctionality(t *testing.T) {
	h := state.NewHistory()

	entry := state.HistoryEntry{
		ContainerID: "test-container",
		Port:        8080,
		StartedAt:   time.Now().Add(-5 * time.Minute),
		EndedAt:     time.Now(),
		EndReason:   "container stopped",
		FinalStatus: "active",
	}

	h.Add(entry)

	entries := h.GetAll()
	assert.Equal(t, 1, h.Count(), "Should have 1 entry")
	assert.Equal(t, 1, len(entries), "GetAll should return 1 entry")
	assert.Equal(t, "test-container", entries[0].ContainerID)
	assert.Equal(t, 8080, entries[0].Port)
	assert.Equal(t, "container stopped", entries[0].EndReason)
}

func TestHistoryAdd_AgeTrimming(t *testing.T) {
	h := state.NewHistory()

	// Add old entry (should be trimmed)
	oldEntry := state.HistoryEntry{
		ContainerID: "old-container",
		Port:        8080,
		StartedAt:   time.Now().Add(-2 * time.Hour),
		EndedAt:     time.Now().Add(-2 * time.Hour),
		EndReason:   "test",
		FinalStatus: "active",
	}
	h.Add(oldEntry)

	// Add recent entry (should remain)
	recentEntry := state.HistoryEntry{
		ContainerID: "recent-container",
		Port:        9090,
		StartedAt:   time.Now().Add(-30 * time.Minute),
		EndedAt:     time.Now().Add(-30 * time.Minute),
		EndReason:   "test",
		FinalStatus: "active",
	}
	h.Add(recentEntry)

	entries := h.GetAll()
	assert.Equal(t, 1, len(entries), "Should only have recent entry (old trimmed by age)")
	assert.Equal(t, "recent-container", entries[0].ContainerID)
}

func TestHistoryAdd_SizeTrimming(t *testing.T) {
	h := state.NewHistory()

	// Add 150 entries
	now := time.Now()
	for i := 0; i < 150; i++ {
		entry := state.HistoryEntry{
			ContainerID: "container-" + string(rune(i)),
			Port:        8080 + i,
			StartedAt:   now.Add(-time.Duration(i) * time.Minute),
			EndedAt:     now.Add(-time.Duration(i) * time.Minute),
			EndReason:   "test",
			FinalStatus: "active",
		}
		h.Add(entry)
	}

	entries := h.GetAll()
	assert.Equal(t, 60, len(entries), "Should only keep most recent 60 entries")
	assert.Equal(t, 60, h.Count(), "Count should return 60")
}

func TestHistoryAdd_CombinedTrimming(t *testing.T) {
	h := state.NewHistory()
	now := time.Now()

	// Add 50 old entries (>1 hour, should be trimmed by age)
	for i := 0; i < 50; i++ {
		entry := state.HistoryEntry{
			ContainerID: "old-" + string(rune(i)),
			Port:        8000 + i,
			StartedAt:   now.Add(-2 * time.Hour),
			EndedAt:     now.Add(-2 * time.Hour),
			EndReason:   "old",
			FinalStatus: "active",
		}
		h.Add(entry)
	}

	// Add 60 recent entries (should remain)
	for i := 0; i < 60; i++ {
		entry := state.HistoryEntry{
			ContainerID: "recent-" + string(rune(i)),
			Port:        9000 + i,
			StartedAt:   now.Add(-30 * time.Minute),
			EndedAt:     now.Add(-30 * time.Minute),
			EndReason:   "recent",
			FinalStatus: "active",
		}
		h.Add(entry)
	}

	entries := h.GetAll()
	assert.LessOrEqual(t, len(entries), 100, "Should not exceed 100 entries")
	
	// Verify no old entries remain
	for _, e := range entries {
		assert.Contains(t, e.ContainerID, "recent-", "Should only have recent entries")
	}
}

func TestHistoryGetAll_ReturnsCopy(t *testing.T) {
	h := state.NewHistory()

	entry := state.HistoryEntry{
		ContainerID: "test",
		Port:        8080,
		StartedAt:   time.Now(),
		EndedAt:     time.Now(),
		EndReason:   "test",
		FinalStatus: "active",
	}
	h.Add(entry)

	// Get entries and modify
	entries1 := h.GetAll()
	entries1[0].EndReason = "modified"

	// Get again and verify original is unchanged
	entries2 := h.GetAll()
	assert.Equal(t, "test", entries2[0].EndReason, "Modification should not affect internal state")
}

func TestHistoryClear(t *testing.T) {
	h := state.NewHistory()

	// Add entries
	for i := 0; i < 10; i++ {
		h.Add(state.HistoryEntry{
			ContainerID: "test",
			Port:        8080 + i,
			StartedAt:   time.Now(),
			EndedAt:     time.Now(),
			EndReason:   "test",
			FinalStatus: "active",
		})
	}

	assert.Equal(t, 10, h.Count())

	h.Clear()

	assert.Equal(t, 0, h.Count())
	assert.Equal(t, 0, len(h.GetAll()))
}

func TestHistoryConcurrency(t *testing.T) {
	h := state.NewHistory()
	done := make(chan bool, 3)

	// Goroutine 1: Add entries
	go func() {
		for i := 0; i < 50; i++ {
			h.Add(state.HistoryEntry{
				ContainerID: "container-1",
				Port:        8000 + i,
				StartedAt:   time.Now(),
				EndedAt:     time.Now(),
				EndReason:   "test",
				FinalStatus: "active",
			})
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Goroutine 2: Read entries
	go func() {
		for i := 0; i < 50; i++ {
			_ = h.GetAll()
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Goroutine 3: Add more entries
	go func() {
		for i := 0; i < 50; i++ {
			h.Add(state.HistoryEntry{
				ContainerID: "container-2",
				Port:        9000 + i,
				StartedAt:   time.Now(),
				EndedAt:     time.Now(),
				EndReason:   "test",
				FinalStatus: "active",
			})
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done

	// Verify final state is valid
	entries := h.GetAll()
	assert.LessOrEqual(t, len(entries), 100, "Should not exceed max size")
	assert.Greater(t, len(entries), 0, "Should have some entries")
}