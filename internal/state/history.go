package state

import (
	"sync"
	"time"
)

// HistoryEntry represents a port forward that has ended
type HistoryEntry struct {
	ContainerID string
	Port        int
	StartedAt   time.Time
	EndedAt     time.Time
	EndReason   string // Why it ended
	FinalStatus string // Status before removal ("active", "conflict", etc.)
}

// History manages historical port forward entries with automatic cleanup.
// It maintains a ring buffer of up to 100 entries or 1 hour of history,
// whichever is less.
type History struct {
	entries []HistoryEntry
	maxSize int
	maxAge  time.Duration
	mu      sync.RWMutex
}

// NewHistory creates a new History instance with default limits:
// - Maximum 100 entries
// - Maximum 1 hour retention
func NewHistory() *History {
	return &History{
		entries: make([]HistoryEntry, 0, 100),
		maxSize: 100,
		maxAge:  time.Hour,
	}
}

// Add adds a new history entry and automatically trims old entries.
// Entries older than 1 hour are removed, and if more than 100 entries
// remain, only the most recent 100 are kept.
func (h *History) Add(entry HistoryEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Add to end
	h.entries = append(h.entries, entry)

	// Trim entries older than maxAge
	now := time.Now()
	cutoff := now.Add(-h.maxAge)

	filtered := make([]HistoryEntry, 0, len(h.entries))
	for _, e := range h.entries {
		if e.EndedAt.After(cutoff) {
			filtered = append(filtered, e)
		}
	}
	h.entries = filtered

	// Trim if exceeds maxSize (keep most recent)
	if len(h.entries) > h.maxSize {
		h.entries = h.entries[len(h.entries)-h.maxSize:]
	}
}

// GetAll returns all history entries as a copy.
// The returned slice is safe to modify without affecting the internal state.
func (h *History) GetAll() []HistoryEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]HistoryEntry, len(h.entries))
	copy(result, h.entries)
	return result
}

// Clear removes all history entries.
func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.entries = make([]HistoryEntry, 0, h.maxSize)
}

// Count returns the current number of history entries.
func (h *History) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.entries)
}
