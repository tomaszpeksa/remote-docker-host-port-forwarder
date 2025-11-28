package statefile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"golang.org/x/sys/unix"
)

// Writer writes state snapshots to a file on disk
type Writer struct {
	path      string
	host      string
	pid       int
	startedAt time.Time
	mu        sync.Mutex
}

// NewWriter creates a new state file writer for the given host
func NewWriter(host string, startedAt time.Time) (*Writer, error) {
	path, err := GetStateFilePath(host)
	if err != nil {
		return nil, err
	}

	return &Writer{
		path:      path,
		host:      host,
		pid:       os.Getpid(),
		startedAt: startedAt,
	}, nil
}

// Write writes the current state snapshot to disk with file locking
func (w *Writer) Write(forwards []state.ForwardState, history []state.HistoryEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Convert to snapshot format
	forwardSnapshots := make([]ForwardSnapshot, len(forwards))
	for i, f := range forwards {
		forwardSnapshots[i] = FromForwardState(f)
	}

	historySnapshots := make([]HistorySnapshot, len(history))
	for i, h := range history {
		historySnapshots[i] = FromHistoryEntry(h)
	}

	snapshot := StateFile{
		Version:   CurrentVersion,
		Host:      w.host,
		PID:       w.pid,
		StartedAt: w.startedAt,
		UpdatedAt: time.Now(),
		Forwards:  forwardSnapshots,
		History:   historySnapshots,
	}

	return w.writeAtomic(snapshot)
}

// writeAtomic writes the state file atomically using a temp file + rename
func (w *Writer) writeAtomic(snapshot StateFile) error {
	// Create temp file in same directory for atomic rename
	tmpFile, err := os.CreateTemp(filepath.Dir(w.path), ".rdhpf-state-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	// Acquire exclusive lock on temp file
	if err := unix.Flock(int(tmpFile.Fd()), unix.LOCK_EX); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("failed to lock temp file: %w", err)
	}

	// Write JSON to temp file
	encoder := json.NewEncoder(tmpFile)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		_ = unix.Flock(int(tmpFile.Fd()), unix.LOCK_UN)
		_ = tmpFile.Close()
		return fmt.Errorf("failed to encode state: %w", err)
	}

	// Sync to disk
	if err := tmpFile.Sync(); err != nil {
		_ = unix.Flock(int(tmpFile.Fd()), unix.LOCK_UN)
		_ = tmpFile.Close()
		return fmt.Errorf("failed to sync temp file: %w", err)
	}

	// Release lock and close
	_ = unix.Flock(int(tmpFile.Fd()), unix.LOCK_UN)
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, w.path); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

// Delete removes the state file from disk
func (w *Writer) Delete() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := os.Remove(w.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete state file: %w", err)
	}

	return nil
}

// Close is an alias for Delete for interface compatibility
func (w *Writer) Close() error {
	return w.Delete()
}
