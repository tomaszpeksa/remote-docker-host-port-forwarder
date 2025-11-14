package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
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
	// Import ssh package is already present at the top

	// Remove ssh:// prefix and parse port for SSH command
	sshHostClean, port, err := ssh.ParseHost(sshHost)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH host: %w", err)
	}

	// Build the docker command as a single quoted string to protect {{json ...}} from shell expansion
	dockerCmd := fmt.Sprintf("docker inspect %s --format '{{json .HostConfig.PortBindings}}'", containerID)

	// Build SSH command that executes docker via sh -c
	// Important: sh -c and the docker command must be passed as a single argument to SSH
	remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)
	args := []string{"-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshHostClean, remoteCmd)

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

	// If no published ports found, check for rdhpf.forward.* labels
	// This supports test containers that don't publish ports to avoid conflicts
	// Only enabled if RDHPF_ENABLE_LABEL_PORTS=1 is set
	if len(ports) == 0 && os.Getenv("RDHPF_ENABLE_LABEL_PORTS") == "1" {
		labelPorts, err := inspectPortsFromLabels(ctx, sshHost, controlPath, containerID)
		if err == nil && len(labelPorts) > 0 {
			return labelPorts, nil
		}
	}

	// Note: We deliberately don't log exposed-only ports at INFO level
	// to avoid noise. They are visible at DEBUG level if needed.

	return ports, nil
}

// inspectPortsFromLabels retrieves port mappings from rdhpf.forward.* labels.
// Labels format: rdhpf.forward.LOCAL_PORT=CONTAINER_PORT
// Returns the LOCAL_PORT values (what to forward to on localhost).
func inspectPortsFromLabels(ctx context.Context, sshHost, controlPath, containerID string) ([]int, error) {
	sshHostClean, port, err := ssh.ParseHost(sshHost)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH host: %w", err)
	}

	// Get container labels
	dockerCmd := fmt.Sprintf("docker inspect %s --format '{{json .Config.Labels}}'", containerID)
	remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)
	args := []string{"-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshHostClean, remoteCmd)

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect labels: %w", err)
	}

	// Parse labels JSON
	var labels map[string]string
	if err := json.Unmarshal(output, &labels); err != nil {
		return nil, fmt.Errorf("failed to parse labels JSON: %w", err)
	}

	// Extract ports from rdhpf.forward.* labels
	ports := make([]int, 0)
	seen := make(map[int]bool)

	for key := range labels {
		if strings.HasPrefix(key, LabelForwardPrefix) {
			// Extract LOCAL_PORT from label key
			portStr := strings.TrimPrefix(key, LabelForwardPrefix)
			localPort, err := strconv.Atoi(portStr)
			if err != nil {
				continue // Skip invalid port numbers
			}

			if !seen[localPort] {
				ports = append(ports, localPort)
				seen[localPort] = true
			}
		}
	}

	return ports, nil
}
