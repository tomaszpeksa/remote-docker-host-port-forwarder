package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// portBindingJSON represents the structure of Docker's PortBindings
// The format is: map[port/protocol][]HostConfig
// Example: {"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8080"}]}
type portBindingJSON map[string][]struct {
	HostIp   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// InspectPorts retrieves the published host ports for a Docker container.
//
// It executes `docker inspect` via SSH to get the container's PortBindings
// and extracts only the published host ports (those with HostPort set).
// Exposed-only ports (without HostPort) are ignored.
//
// Parameters:
//   - ctx: Context for cancellation
//   - sshHost: SSH connection string in ssh://user@host format
//   - controlPath: Path to SSH control socket
//   - containerID: Full or short container ID
//
// Returns:
//   - Slice of published port numbers (integers)
//   - Empty slice if container has no published ports
//   - Error if container doesn't exist or command fails
//
// Example usage:
//
//	ctx := context.Background()
//	ports, err := InspectPorts(ctx, "ssh://user@host", "/tmp/rdhpf-abc.sock", "container123")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Published ports: %v\n", ports) // [8080, 9090]
func InspectPorts(ctx context.Context, sshHost, controlPath, containerID string) ([]int, error) {
	// Remove ssh:// prefix for SSH command
	sshHostClean := strings.TrimPrefix(sshHost, "ssh://")

	// Build the docker command as a single quoted string to protect {{json ...}} from shell expansion
	dockerCmd := fmt.Sprintf("docker inspect %s --format '{{json .HostConfig.PortBindings}}'", containerID)

	// Build SSH command that executes docker via sh -c
	args := []string{
		"-S", controlPath,
		sshHostClean,
		"sh", "-c", dockerCmd,
	}

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	output, err := cmd.Output()
	if err != nil {
		// Check if it's a non-existent container error
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := string(exitErr.Stderr)
			if strings.Contains(stderr, "No such object") || strings.Contains(stderr, "No such container") {
				return nil, fmt.Errorf("container not found: %s", containerID)
			}
			return nil, fmt.Errorf("docker inspect failed: %s", stderr)
		}
		return nil, fmt.Errorf("failed to execute docker inspect: %w", err)
	}

	// Parse PortBindings JSON
	var portBindings portBindingJSON
	if err := json.Unmarshal(output, &portBindings); err != nil {
		return nil, fmt.Errorf("failed to parse PortBindings JSON: %w", err)
	}

	// Extract published host ports
	// Note: Only ports with HostPort set (published via -p flag) are returned.
	// Ports with only EXPOSE (no -p) have empty HostPort and are explicitly ignored.
	ports := make([]int, 0)
	seen := make(map[int]bool) // Deduplicate ports
	exposedOnly := 0

	for _, bindings := range portBindings {
		for _, binding := range bindings {
			// Skip if no host port is set (exposed-only)
			if binding.HostPort == "" {
				exposedOnly++
				continue
			}

			// Parse host port
			port, err := strconv.Atoi(binding.HostPort)
			if err != nil {
				// Skip invalid port numbers
				continue
			}

			// Add to result if not seen before
			if !seen[port] {
				ports = append(ports, port)
				seen[port] = true
			}
		}
	}

	// Note: We deliberately don't log exposed-only ports at INFO level
	// to avoid noise. They are visible at DEBUG level if needed.

	return ports, nil
}
