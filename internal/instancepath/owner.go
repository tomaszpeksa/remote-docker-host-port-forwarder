package instancepath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Owner admits one publisher per actual HOME/.rdhpf directory, not per target.
// The cooperative contract requires local Linux/macOS storage and stable paths.
// Never unlink owner.lock: contenders must always lock the same persistent inode.
type Owner struct {
	mu         sync.Mutex
	file       *os.File
	host       string
	socketPath string
	statePath  string
}

// Acquire must precede SSH startup and publication. Close only after all
// publishers/pathname closers have joined and final cleanup has completed.
func Acquire(host string) (*Owner, error) {
	socketPath, err := StatusPath(host, ".sock")
	if err != nil {
		return nil, err
	}
	directory, err := openOwnerDirectory(filepath.Dir(socketPath))
	if err != nil {
		return nil, fmt.Errorf("cannot open publisher directory: %w", err)
	}
	defer directory.Close()
	file, err := openOwnerFile(directory)
	if err != nil {
		return nil, fmt.Errorf("cannot acquire publisher ownership: %w", err)
	}
	return &Owner{file: file, host: host, socketPath: socketPath, statePath: strings.TrimSuffix(socketPath, ".sock") + ".state.json"}, nil
}

// Paths freezes the admitted publication addresses; Manager checks the host
// before initializing either publisher. Readers retain the existing wrappers.
func (o *Owner) Paths(host string) (string, string, error) {
	if o == nil {
		return "", "", fmt.Errorf("publisher admission required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.file == nil || host != o.host {
		return "", "", fmt.Errorf("publisher admission closed or target mismatch")
	}
	return o.socketPath, o.statePath, nil
}

func (o *Owner) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.file == nil {
		return nil
	}
	err := o.file.Close()
	o.file = nil
	return err
}
