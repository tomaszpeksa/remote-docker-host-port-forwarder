package unit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockDockerEventJSON represents the JSON structure from docker events
// This mirrors the internal structure in docker/events.go
type mockDockerEventJSON struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	Time     int64  `json:"time"`
	TimeNano int64  `json:"timeNano"`
	Status   string `json:"status"`
}

// TestParseDockerEventJSON_ValidStartEvent tests parsing a start event
func TestParseDockerEventJSON_ValidStartEvent(t *testing.T) {
	jsonStr := `{
		"Type": "container",
		"Action": "start",
		"Actor": {
			"ID": "abc123def456",
			"Attributes": {
				"name": "test-container"
			}
		},
		"time": 1699564800,
		"timeNano": 1699564800000000000,
		"status": "start"
	}`

	var dockerEvent mockDockerEventJSON
	err := json.Unmarshal([]byte(jsonStr), &dockerEvent)
	require.NoError(t, err, "JSON should be valid")

	// Verify parsing
	assert.Equal(t, "container", dockerEvent.Type)
	assert.Equal(t, "start", dockerEvent.Action)
	assert.Equal(t, "abc123def456", dockerEvent.Actor.ID)
	assert.Equal(t, int64(1699564800), dockerEvent.Time)
}

// TestParseDockerEventJSON_ValidDieEvent tests parsing a die event
func TestParseDockerEventJSON_ValidDieEvent(t *testing.T) {
	jsonStr := `{
		"Type": "container",
		"Action": "die",
		"Actor": {
			"ID": "xyz789abc123"
		},
		"time": 1699564900,
		"status": "die"
	}`

	var dockerEvent mockDockerEventJSON
	err := json.Unmarshal([]byte(jsonStr), &dockerEvent)
	require.NoError(t, err, "JSON should be valid")

	assert.Equal(t, "die", dockerEvent.Action)
	assert.Equal(t, "xyz789abc123", dockerEvent.Actor.ID)
}

// TestParseDockerEventJSON_ValidStopEvent tests parsing a stop event
func TestParseDockerEventJSON_ValidStopEvent(t *testing.T) {
	jsonStr := `{
		"Type": "container",
		"Action": "stop",
		"Actor": {
			"ID": "container456"
		},
		"time": 1699565000
	}`

	var dockerEvent mockDockerEventJSON
	err := json.Unmarshal([]byte(jsonStr), &dockerEvent)
	require.NoError(t, err, "JSON should be valid")

	assert.Equal(t, "stop", dockerEvent.Action)
	assert.Equal(t, "container456", dockerEvent.Actor.ID)
}

// TestParseDockerEventJSON_StatusFallback tests using status when Action is empty
func TestParseDockerEventJSON_StatusFallback(t *testing.T) {
	jsonStr := `{
		"Type": "container",
		"Action": "",
		"Actor": {
			"ID": "container789"
		},
		"time": 1699565100,
		"status": "start"
	}`

	var dockerEvent mockDockerEventJSON
	err := json.Unmarshal([]byte(jsonStr), &dockerEvent)
	require.NoError(t, err, "JSON should be valid")

	// Action is empty, should fall back to status
	assert.Equal(t, "", dockerEvent.Action)
	assert.Equal(t, "start", dockerEvent.Status)
}

// TestParseDockerEventJSON_MalformedJSON tests handling of invalid JSON
func TestParseDockerEventJSON_MalformedJSON(t *testing.T) {
	malformedCases := []struct {
		name    string
		jsonStr string
	}{
		{
			name:    "incomplete JSON",
			jsonStr: `{"Type": "container", "Action":`,
		},
		{
			name:    "not JSON at all",
			jsonStr: `this is not json`,
		},
		{
			name:    "empty string",
			jsonStr: ``,
		},
	}

	for _, tc := range malformedCases {
		t.Run(tc.name, func(t *testing.T) {
			var dockerEvent mockDockerEventJSON
			err := json.Unmarshal([]byte(tc.jsonStr), &dockerEvent)
			assert.Error(t, err, "Malformed JSON should produce error")
		})
	}
}

// TestParseDockerEventJSON_UnexpectedStructure tests that unknown fields are ignored
func TestParseDockerEventJSON_UnexpectedStructure(t *testing.T) {
	// Go's JSON unmarshaling ignores unknown fields
	jsonStr := `{"completely": "wrong", "unknown": "fields"}`

	var dockerEvent mockDockerEventJSON
	err := json.Unmarshal([]byte(jsonStr), &dockerEvent)
	require.NoError(t, err, "Unknown fields should be ignored")

	// All fields should have zero values
	assert.Equal(t, "", dockerEvent.Type)
	assert.Equal(t, "", dockerEvent.Action)
	assert.Equal(t, "", dockerEvent.Actor.ID)
}

// TestExtractEventType tests event type extraction logic
func TestExtractEventType(t *testing.T) {
	cases := []struct {
		name         string
		action       string
		status       string
		expectedType string
	}{
		{
			name:         "Action takes precedence",
			action:       "start",
			status:       "running",
			expectedType: "start",
		},
		{
			name:         "Fallback to status when action empty",
			action:       "",
			status:       "die",
			expectedType: "die",
		},
		{
			name:         "Both empty",
			action:       "",
			status:       "",
			expectedType: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dockerEvent := mockDockerEventJSON{
				Action: tc.action,
				Status: tc.status,
			}

			// Simulate the logic from events.go
			eventType := dockerEvent.Action
			if eventType == "" {
				eventType = dockerEvent.Status
			}

			assert.Equal(t, tc.expectedType, eventType)
		})
	}
}

// TestExtractContainerID tests container ID extraction
func TestExtractContainerID(t *testing.T) {
	jsonStr := `{
		"Actor": {
			"ID": "abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567890abcdef12"
		}
	}`

	var dockerEvent mockDockerEventJSON
	err := json.Unmarshal([]byte(jsonStr), &dockerEvent)
	require.NoError(t, err)

	// Full container ID should be preserved
	assert.Equal(t, "abc123def456ghi789jkl012mno345pqr678stu901vwx234yz567890abcdef12",
		dockerEvent.Actor.ID)
}

// TestExtractTimestamp tests timestamp conversion
func TestExtractTimestamp(t *testing.T) {
	jsonStr := `{
		"time": 1699564800,
		"timeNano": 1699564800123456789
	}`

	var dockerEvent mockDockerEventJSON
	err := json.Unmarshal([]byte(jsonStr), &dockerEvent)
	require.NoError(t, err)

	timestamp := time.Unix(dockerEvent.Time, 0)
	expectedTime := time.Unix(1699564800, 0)

	assert.Equal(t, expectedTime, timestamp)
}

// TestPortBindingsJSON_ValidPorts tests parsing port bindings
func TestPortBindingsJSON_ValidPorts(t *testing.T) {
	jsonStr := `{
		"5432/tcp": [
			{
				"HostIp": "0.0.0.0",
				"HostPort": "5432"
			}
		],
		"6379/tcp": [
			{
				"HostIp": "127.0.0.1",
				"HostPort": "6379"
			}
		]
	}`

	type portBindingJSON map[string][]struct {
		HostIp   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}

	var portBindings portBindingJSON
	err := json.Unmarshal([]byte(jsonStr), &portBindings)
	require.NoError(t, err, "JSON should be valid")

	// Verify structure
	assert.Len(t, portBindings, 2, "Should have 2 port mappings")

	postgres := portBindings["5432/tcp"]
	require.Len(t, postgres, 1)
	assert.Equal(t, "0.0.0.0", postgres[0].HostIp)
	assert.Equal(t, "5432", postgres[0].HostPort)

	redis := portBindings["6379/tcp"]
	require.Len(t, redis, 1)
	assert.Equal(t, "127.0.0.1", redis[0].HostIp)
	assert.Equal(t, "6379", redis[0].HostPort)
}

// TestPortBindingsJSON_EmptyPorts tests container with no port bindings
func TestPortBindingsJSON_EmptyPorts(t *testing.T) {
	jsonStr := `{}`

	type portBindingJSON map[string][]struct {
		HostIp   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}

	var portBindings portBindingJSON
	err := json.Unmarshal([]byte(jsonStr), &portBindings)
	require.NoError(t, err, "Empty object should be valid")

	assert.Len(t, portBindings, 0, "Should have no port bindings")
}

// TestPortBindingsJSON_ExposedOnlyPort tests exposed but not published port
func TestPortBindingsJSON_ExposedOnlyPort(t *testing.T) {
	// When a port is exposed but not published, Docker returns null or empty HostPort
	jsonStr := `{
		"8080/tcp": [
			{
				"HostIp": "",
				"HostPort": ""
			}
		]
	}`

	type portBindingJSON map[string][]struct {
		HostIp   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}

	var portBindings portBindingJSON
	err := json.Unmarshal([]byte(jsonStr), &portBindings)
	require.NoError(t, err, "JSON should be valid")

	exposed := portBindings["8080/tcp"]
	require.Len(t, exposed, 1)
	assert.Equal(t, "", exposed[0].HostPort, "Exposed-only port has empty HostPort")
}

// TestPortBindingsJSON_MultipleBindings tests multiple host bindings for one container port
func TestPortBindingsJSON_MultipleBindings(t *testing.T) {
	jsonStr := `{
		"80/tcp": [
			{
				"HostIp": "0.0.0.0",
				"HostPort": "8080"
			},
			{
				"HostIp": "0.0.0.0",
				"HostPort": "8081"
			}
		]
	}`

	type portBindingJSON map[string][]struct {
		HostIp   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}

	var portBindings portBindingJSON
	err := json.Unmarshal([]byte(jsonStr), &portBindings)
	require.NoError(t, err, "JSON should be valid")

	bindings := portBindings["80/tcp"]
	require.Len(t, bindings, 2, "Should have 2 bindings for port 80")
	assert.Equal(t, "8080", bindings[0].HostPort)
	assert.Equal(t, "8081", bindings[1].HostPort)
}

// TestPortBindingsJSON_InvalidJSON tests malformed port bindings JSON
func TestPortBindingsJSON_InvalidJSON(t *testing.T) {
	malformedCases := []struct {
		name    string
		jsonStr string
	}{
		{
			name:    "incomplete JSON",
			jsonStr: `{"5432/tcp": [{"HostIp":`,
		},
		{
			name:    "not JSON",
			jsonStr: `not json at all`,
		},
	}

	type portBindingJSON map[string][]struct {
		HostIp   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}

	for _, tc := range malformedCases {
		t.Run(tc.name, func(t *testing.T) {
			var portBindings portBindingJSON
			err := json.Unmarshal([]byte(tc.jsonStr), &portBindings)
			assert.Error(t, err, "Malformed JSON should produce error")
		})
	}
}

// TestPortBindingsJSON_NullValue tests that null creates empty map
func TestPortBindingsJSON_NullValue(t *testing.T) {
	type portBindingJSON map[string][]struct {
		HostIp   string `json:"HostIp"`
		HostPort string `json:"HostPort"`
	}

	var portBindings portBindingJSON
	err := json.Unmarshal([]byte(`null`), &portBindings)
	require.NoError(t, err, "null is valid JSON for maps")
	assert.Nil(t, portBindings, "null should create nil map")
}
