package ssh

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// DeriveControlPath generates a stable control socket path for an SSH host.
// The path is deterministic based on the host string to ensure the same host
// always uses the same control socket.
//
// Parameters:
//   - host: SSH connection string in ssh://user@host format
//
// Returns:
//   - A path like /tmp/rdhpf-{hash}.sock where hash is the first 16 hex chars
//     of SHA256(host)
//   - Error if the host format is invalid
//
// The generated socket file will be created by SSH with 0600 permissions.
//
// Example usage:
//
//	path, err := DeriveControlPath("ssh://user@example.com")
//	// Returns: /tmp/rdhpf-a1b2c3d4e5f60708.sock, nil
//
//	path, err := DeriveControlPath("invalid")
//	// Returns: "", error
func DeriveControlPath(host string) (string, error) {
	// Validate ssh:// prefix
	if !strings.HasPrefix(host, "ssh://") {
		return "", fmt.Errorf("host must be in ssh://user@host format, got: %s", host)
	}

	// Remove the ssh:// prefix for validation
	hostPart := strings.TrimPrefix(host, "ssh://")
	if hostPart == "" {
		return "", fmt.Errorf("host cannot be empty after ssh:// prefix")
	}

	// Generate deterministic hash from the full host string
	hash := sha256.Sum256([]byte(host))
	// Use first 16 hex characters (8 bytes) for the filename
	hashStr := fmt.Sprintf("%x", hash[:8])

	// Construct the control socket path
	controlPath := fmt.Sprintf("/tmp/rdhpf-%s.sock", hashStr)

	return controlPath, nil
}
