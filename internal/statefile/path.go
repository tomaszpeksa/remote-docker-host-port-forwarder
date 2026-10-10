package statefile

import "github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"

// GetStateFilePath returns the path to the state file for a given host.
// The path is ~/.rdhpf/{host-hash}.state.json where host-hash is a
// base64-encoded SHA256 hash of the host string (truncated to 12 chars).
func GetStateFilePath(host string) (string, error) {
	return instancepath.StatusPath(host, ".state.json")
}
