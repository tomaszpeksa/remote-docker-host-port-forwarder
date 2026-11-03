package statefile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Reader reads state snapshots from disk.
type Reader struct {
	path string
	host string
}

// NewReader creates a new state file reader for the given host.
func NewReader(host string) (*Reader, error) {
	path, err := GetStateFilePath(host)
	if err != nil {
		return nil, err
	}
	return &Reader{path: path, host: host}, nil
}

// Read reads a snapshot with the ordinary five-second observation budget.
func (r *Reader) Read() (*StateFile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.ReadContext(ctx)
}

// ReadContext acquires the existing shared reader lock without blocking beyond
// the caller's budget. Only ordinary local regular snapshot files are read;
// nonblocking open prevents a substituted pipe from hanging before validation.
func (r *Reader) ReadContext(ctx context.Context) (*StateFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(r.path, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("status snapshot is not a regular file")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err = unix.Flock(int(file.Fd()), unix.LOCK_SH|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, fmt.Errorf("failed to lock state file: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }()
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var snapshot StateFile
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("failed to decode state file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := snapshot.Validate(r.host); err != nil {
		return nil, err
	}
	return &snapshot, nil
}
