package statefile

import (
	"encoding/json"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Reader reads state snapshots from disk
type Reader struct {
	path string
}

// NewReader creates a new state file reader for the given host
func NewReader(host string) (*Reader, error) {
	path, err := GetStateFilePath(host)
	if err != nil {
		return nil, err
	}

	return &Reader{
		path: path,
	}, nil
}

// Read reads and parses the state file from disk with file locking
func (r *Reader) Read() (*StateFile, error) {
	file, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Acquire shared lock for reading
	if err := unix.Flock(int(file.Fd()), unix.LOCK_SH); err != nil {
		return nil, fmt.Errorf("failed to lock state file: %w", err)
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)

	var snapshot StateFile
	if err := json.NewDecoder(file).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("failed to decode state file: %w", err)
	}

	return &snapshot, nil
}