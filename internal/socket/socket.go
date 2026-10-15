package socket

import "github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"

// GetSocketPath returns the path to the Unix socket for a given host.
// The path is ~/.rdhpf/{host-hash}.sock where host-hash is a
// base64-encoded SHA256 hash of the host string (truncated to 12 chars).
func GetSocketPath(host string) (string, error) {
	return instancepath.StatusPath(host, ".sock")
}
