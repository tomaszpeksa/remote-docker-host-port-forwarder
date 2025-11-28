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
	IsHistory   bool          `json:"is_history" yaml:"is_history"`
	EndedAt     *time.Time    `json:"ended_at,omitempty" yaml:"ended_at,omitempty"`
}

// ForwardJSON is the JSON representation with duration as string
type forwardJSON struct {
	ContainerID string  `json:"container_id"`
	LocalPort   int     `json:"local_port"`
	RemotePort  int     `json:"remote_port"`
	State       string  `json:"state"`
	Duration    string  `json:"duration"`
	Reason      string  `json:"reason,omitempty"`
	IsHistory   bool    `json:"is_history"`
	EndedAt     *string `json:"ended_at,omitempty"`
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
		IsHistory:   f.IsHistory,
	}
	if f.EndedAt != nil {
		endedStr := f.EndedAt.Format(time.RFC3339)
		fj.EndedAt = &endedStr
	}
	return json.Marshal(fj)
}

// MarshalYAML implements custom YAML marshaling for Forward
func (f Forward) MarshalYAML() (interface{}, error) {
	result := map[string]interface{}{
		"container_id": f.ContainerID,
		"local_port":   f.LocalPort,
		"remote_port":  f.RemotePort,
		"state":        f.State,
		"duration":     f.Duration.String(),
		"reason":       f.Reason,
		"is_history":   f.IsHistory,
	}
	if f.EndedAt != nil {
		result["ended_at"] = f.EndedAt.Format(time.RFC3339)
	}
	return result, nil
}

// StatusOutput represents the complete status output structure
type StatusOutput struct {
	Forwards []Forward `json:"forwards" yaml:"forwards"`
}

// FormatTable formats forwards as a human-readable table with current + history
func FormatTable(forwards []Forward) string {
	if len(forwards) == 0 {
		return "No forwards\n"
	}

	var sb strings.Builder

	// Header
	sb.WriteString(fmt.Sprintf("%-16s %-8s %-10s %-16s %-16s %s\n",
		"CONTAINER", "PORT", "STATUS", "STARTED", "ENDED", "REASON"))
	sb.WriteString(strings.Repeat("-", 100))
	sb.WriteString("\n")

	// Rows
	for _, f := range forwards {
		// Truncate container ID to 16 chars or first 12 chars
		containerID := f.ContainerID
		if len(containerID) > 16 {
			if len(containerID) >= 12 {
				containerID = containerID[:12]
			} else {
				containerID = containerID[:16]
			}
		}

		port := fmt.Sprintf("%d", f.LocalPort)
		status := f.State
		started := formatTimeAgo(f.Duration, f.IsHistory)
		ended := "-"
		if f.EndedAt != nil {
			ended = formatTimeAgo(time.Since(*f.EndedAt), false)
		}
		reason := f.Reason

		sb.WriteString(fmt.Sprintf("%-16s %-8s %-10s %-16s %-16s %s\n",
			containerID, port, status, started, ended, reason))
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

// formatTimeAgo formats a duration as "X ago" for display
func formatTimeAgo(d time.Duration, isLifetime bool) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	// Shouldn't happen with 1 hour history limit
	days := int(d.Hours() / 24)
	return fmt.Sprintf("%dd ago", days)
}
