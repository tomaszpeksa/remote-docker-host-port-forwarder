package socket

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
)

// GetSocketPath returns the path to the Unix socket for a given host.
// The path is ~/.rdhpf/{host-hash}.sock where host-hash is a
// base64-encoded SHA256 hash of the host string (truncated to 12 chars).
func GetSocketPath(host string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	rdhpfDir := filepath.Join(homeDir, ".rdhpf")
	if err := os.MkdirAll(rdhpfDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create .rdhpf directory: %w", err)
	}

	hostHash := hashHost(host)
	return filepath.Join(rdhpfDir, hostHash+".sock"), nil
}

// hashHost creates a short hash of the host string for use in filenames
func hashHost(host string) string {
	h := sha256.Sum256([]byte(host))
	encoded := base64.RawURLEncoding.EncodeToString(h[:])
	// Return first 12 characters for a short but unique identifier
	if len(encoded) > 12 {
		return encoded[:12]
	}
	return encoded
}
