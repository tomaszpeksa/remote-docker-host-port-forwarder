package statefile

import (
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

const (
	// CurrentVersion is the state file format version
	CurrentVersion = "2.0"

	// MaxStateAge defines how old a state file can be before it's considered stale
	MaxStateAge = 10 * time.Second
)

// StateFile represents the complete state snapshot written to disk
type StateFile struct {
	Version   string            `json:"version"`
	Host      string            `json:"host"`
	PID       int               `json:"pid"`
	StartedAt time.Time         `json:"started_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Forwards  []ForwardSnapshot `json:"forwards"`
	History   []HistorySnapshot `json:"history"`
}

// ForwardSnapshot represents a forward in the state file
type ForwardSnapshot struct {
	ContainerID string    `json:"container_id"`
	Port        int       `json:"port"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// HistorySnapshot represents a history entry in the state file
type HistorySnapshot struct {
	ContainerID string    `json:"container_id"`
	Port        int       `json:"port"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	EndReason   string    `json:"end_reason"`
	FinalStatus string    `json:"final_status"`
}

// FromForwardState converts a state.ForwardState to ForwardSnapshot
func FromForwardState(fs state.ForwardState) ForwardSnapshot {
	return ForwardSnapshot{
		ContainerID: fs.ContainerID,
		Port:        fs.Port,
		Status:      fs.Status,
		Reason:      fs.Reason,
		CreatedAt:   fs.CreatedAt,
		UpdatedAt:   fs.UpdatedAt,
	}
}

// FromHistoryEntry converts a state.HistoryEntry to HistorySnapshot
func FromHistoryEntry(he state.HistoryEntry) HistorySnapshot {
	return HistorySnapshot{
		ContainerID: he.ContainerID,
		Port:        he.Port,
		StartedAt:   he.StartedAt,
		EndedAt:     he.EndedAt,
		EndReason:   he.EndReason,
		FinalStatus: he.FinalStatus,
	}
}

// IsStale returns true if the state file is older than MaxStateAge
func (sf *StateFile) IsStale() bool {
	age := time.Since(sf.UpdatedAt)
	return age > MaxStateAge
}