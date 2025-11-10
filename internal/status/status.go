package status

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Forward represents the state of a port forward for status display
type Forward struct {
	ContainerID string        `json:"container_id" yaml:"container_id"`
	LocalPort   int           `json:"local_port" yaml:"local_port"`
	RemotePort  int           `json:"remote_port" yaml:"remote_port"`
	State       string        `json:"state" yaml:"state"`
	Duration    time.Duration `json:"-" yaml:"-"`
	Reason      string        `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// ForwardJSON is the JSON representation with duration as string
type forwardJSON struct {
	ContainerID string `json:"container_id"`
	LocalPort   int    `json:"local_port"`
	RemotePort  int    `json:"remote_port"`
	State       string `json:"state"`
	Duration    string `json:"duration"`
	Reason      string `json:"reason,omitempty"`
}

// MarshalJSON implements custom JSON marshaling for Forward
func (f Forward) MarshalJSON() ([]byte, error) {
	fj := forwardJSON{
		ContainerID: f.ContainerID,
		LocalPort:   f.LocalPort,
		RemotePort:  f.RemotePort,
		State:       f.State,
		Duration:    f.Duration.String(),
		Reason:      f.Reason,
	}
	return json.Marshal(fj)
}

// MarshalYAML implements custom YAML marshaling for Forward
func (f Forward) MarshalYAML() (interface{}, error) {
	return map[string]interface{}{
		"container_id": f.ContainerID,
		"local_port":   f.LocalPort,
		"remote_port":  f.RemotePort,
		"state":        f.State,
		"duration":     f.Duration.String(),
		"reason":       f.Reason,
	}, nil
}

// StatusOutput represents the complete status output structure
type StatusOutput struct {
	Forwards []Forward `json:"forwards" yaml:"forwards"`
}

// FormatTable formats forwards as a human-readable table
func FormatTable(forwards []Forward) string {
	if len(forwards) == 0 {
		return "No active forwards\n"
	}

	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("%-24s %-20s %-12s %-10s %-12s %s\n",
		"CONTAINER ID",
		"LOCAL",
		"REMOTE",
		"STATE",
		"DURATION",
		"REASON"))
	sb.WriteString(strings.Repeat("-", 100))
	sb.WriteString("\n")

	// Rows
	for _, f := range forwards {
		// Truncate container ID if too long
		containerID := f.ContainerID
		if len(containerID) > 24 {
			containerID = containerID[:24]
		}

		local := fmt.Sprintf("127.0.0.1:%d", f.LocalPort)
		remote := fmt.Sprintf("%d", f.RemotePort)
		duration := formatDuration(f.Duration)

		sb.WriteString(fmt.Sprintf("%-24s %-20s %-12s %-10s %-12s %s\n",
			containerID,
			local,
			remote,
			f.State,
			duration,
			f.Reason))
	}

	return sb.String()
}

// FormatJSON formats forwards as JSON
func FormatJSON(forwards []Forward) string {
	output := StatusOutput{Forwards: forwards}

	data, err := json.Marshal(output)
	if err != nil {
		// This should not happen with our simple struct
		return fmt.Sprintf(`{"error": "failed to marshal JSON: %s"}`, err.Error())
	}

	return string(data)
}

// FormatYAML formats forwards as YAML
func FormatYAML(forwards []Forward) string {
	output := StatusOutput{Forwards: forwards}

	data, err := yaml.Marshal(output)
	if err != nil {
		// This should not happen with our simple struct
		return fmt.Sprintf("error: failed to marshal YAML: %s\n", err.Error())
	}

	return string(data)
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}

	// Round to seconds for display
	d = d.Round(time.Second)

	return d.String()
}
