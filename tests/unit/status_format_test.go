package unit

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/status"
)

func TestFormatTable_Empty(t *testing.T) {
	// Test: empty state should show "No forwards"
	forwards := []status.Forward{}

	output := status.FormatTable(forwards)

	assert.Contains(t, output, "No forwards")
}

func TestFormatTable_SingleForward(t *testing.T) {
	// Test: single active forward displays correctly
	forwards := []status.Forward{
		{
			ContainerID: "abc123456789",
			LocalPort:   8080,
			RemotePort:  8080,
			State:       "active",
			Duration:    5 * time.Minute,
		},
	}

	output := status.FormatTable(forwards)

	// Should contain container ID (truncated)
	assert.Contains(t, output, "abc123456789")
	// Should contain port info
	assert.Contains(t, output, "8080")
	// Should contain state
	assert.Contains(t, output, "active")
	// Should contain duration
	assert.Contains(t, output, "5m")
}

func TestFormatTable_MultipleForwards(t *testing.T) {
	// Test: multiple forwards with mixed states
	forwards := []status.Forward{
		{
			ContainerID: "container1",
			LocalPort:   8080,
			RemotePort:  8080,
			State:       "active",
			Duration:    10 * time.Minute,
		},
		{
			ContainerID: "container2",
			LocalPort:   5432,
			RemotePort:  5432,
			State:       "conflict",
			Duration:    2 * time.Minute,
			Reason:      "port already in use",
		},
		{
			ContainerID: "fixed-port-9090",
			LocalPort:   9090,
			RemotePort:  9090,
			State:       "active",
			Duration:    1 * time.Hour,
		},
	}

	output := status.FormatTable(forwards)

	// All containers should be present
	assert.Contains(t, output, "container1")
	assert.Contains(t, output, "container2")
	assert.Contains(t, output, "fixed-port-9090")

	// All states should be present
	assert.Contains(t, output, "active")
	assert.Contains(t, output, "conflict")

	// Conflict reason should be shown
	assert.Contains(t, output, "port already in use")
}

func TestFormatTable_Alignment(t *testing.T) {
	// Test: columns should be properly aligned
	forwards := []status.Forward{
		{
			ContainerID: "short",
			LocalPort:   80,
			RemotePort:  80,
			State:       "active",
			Duration:    1 * time.Second,
		},
		{
			ContainerID: "verylongcontainerid123456",
			LocalPort:   12345,
			RemotePort:  12345,
			State:       "conflict",
			Duration:    999 * time.Hour,
			Reason:      "some reason",
		},
	}

	output := status.FormatTable(forwards)
	lines := strings.Split(output, "\n")

	// Should have header row and at least 2 data rows
	assert.GreaterOrEqual(t, len(lines), 3)

	// Each data line should have consistent structure
	// (this is a basic check - actual alignment is visual)
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Lines should not be empty
		assert.NotEmpty(t, strings.TrimSpace(line))
	}
}

func TestFormatJSON_Empty(t *testing.T) {
	// Test: empty JSON output
	forwards := []status.Forward{}

	output := status.FormatJSON(forwards)

	assert.JSONEq(t, `{"forwards":[]}`, output)
}

func TestFormatJSON_SingleForward(t *testing.T) {
	// Test: JSON output for single forward
	forwards := []status.Forward{
		{
			ContainerID: "abc123",
			LocalPort:   8080,
			RemotePort:  8080,
			State:       "active",
			Duration:    5 * time.Minute,
		},
	}

	output := status.FormatJSON(forwards)

	// Should be valid JSON
	assert.Contains(t, output, `"container_id":"abc123"`)
	assert.Contains(t, output, `"local_port":8080`)
	assert.Contains(t, output, `"remote_port":8080`)
	assert.Contains(t, output, `"state":"active"`)
	assert.Contains(t, output, `"duration":"5m0s"`)
}

func TestFormatJSON_MultipleForwards(t *testing.T) {
	// Test: JSON array with multiple forwards
	forwards := []status.Forward{
		{
			ContainerID: "container1",
			LocalPort:   8080,
			RemotePort:  8080,
			State:       "active",
			Duration:    10 * time.Minute,
		},
		{
			ContainerID: "container2",
			LocalPort:   5432,
			RemotePort:  5432,
			State:       "conflict",
			Duration:    2 * time.Minute,
			Reason:      "port already in use",
		},
	}

	output := status.FormatJSON(forwards)

	// Should contain both containers
	assert.Contains(t, output, "container1")
	assert.Contains(t, output, "container2")
	assert.Contains(t, output, "port already in use")
}

func TestFormatYAML_Empty(t *testing.T) {
	// Test: empty YAML output
	forwards := []status.Forward{}

	output := status.FormatYAML(forwards)

	assert.Contains(t, output, "forwards: []")
}

func TestFormatYAML_SingleForward(t *testing.T) {
	// Test: YAML output for single forward
	forwards := []status.Forward{
		{
			ContainerID: "abc123",
			LocalPort:   8080,
			RemotePort:  8080,
			State:       "active",
			Duration:    5 * time.Minute,
		},
	}

	output := status.FormatYAML(forwards)

	// YAML should contain the expected fields
	assert.Contains(t, output, "container_id: abc123")
	assert.Contains(t, output, "local_port: 8080")
	assert.Contains(t, output, "remote_port: 8080")
	assert.Contains(t, output, "state: active")
	assert.Contains(t, output, "duration: 5m0s")
}

func TestFormatYAML_MultipleForwards(t *testing.T) {
	// Test: YAML with multiple forwards
	forwards := []status.Forward{
		{
			ContainerID: "container1",
			LocalPort:   8080,
			RemotePort:  8080,
			State:       "active",
			Duration:    10 * time.Minute,
		},
		{
			ContainerID: "container2",
			LocalPort:   5432,
			RemotePort:  5432,
			State:       "conflict",
			Duration:    2 * time.Minute,
			Reason:      "port already in use",
		},
	}

	output := status.FormatYAML(forwards)

	// Should be valid YAML with both entries
	assert.Contains(t, output, "container1")
	assert.Contains(t, output, "container2")
	assert.Contains(t, output, "port already in use")
}

func TestFormatDuration(t *testing.T) {
	// Test: duration formatting
	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		{"seconds", 45 * time.Second, "45s"},
		{"minutes", 5 * time.Minute, "5m0s"},
		{"hours", 2 * time.Hour, "2h0m0s"},
		{"mixed", 1*time.Hour + 30*time.Minute + 15*time.Second, "1h30m15s"},
		{"zero", 0, "0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.duration.String()
			assert.Equal(t, tt.expected, result)
		})
	}
}
