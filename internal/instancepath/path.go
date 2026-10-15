// Package instancepath derives paths for per-host status files and sockets.
package instancepath

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// StatusPath returns a raw-host-derived path with suffix inside ~/.rdhpf,
// creating the directory if needed without changing existing permissions.
func StatusPath(host, suffix string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	rdhpfDir := filepath.Join(homeDir, ".rdhpf")
	if err := os.MkdirAll(rdhpfDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create .rdhpf directory: %w", err)
	}

	h := sha256.Sum256([]byte(host))
	encoded := base64.RawURLEncoding.EncodeToString(h[:])
	return filepath.Join(rdhpfDir, encoded[:12]+suffix), nil
}
