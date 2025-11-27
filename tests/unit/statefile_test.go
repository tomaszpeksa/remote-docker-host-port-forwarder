package unit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

func TestStateFilePath_Resolution(t *testing.T) {
	host := "ssh://user@example.com"
	
	path, err := statefile.GetStateFilePath(host)
	require.NoError(t, err)
	
	// Should be in home directory
	homeDir, _ := os.UserHomeDir()
	assert.Contains(t, path, filepath.Join(homeDir, ".rdhpf"))
	
	// Should end with .state.json
	assert.Contains(t, path, ".state.json")
	
	// Path should be consistent for same host
	path2, err := statefile.GetStateFilePath(host)
	require.NoError(t, err)
	assert.Equal(t, path, path2)
	
	// Different host should give different path
	path3, err := statefile.GetStateFilePath("ssh://user@different.com")
	require.NoError(t, err)
	assert.NotEqual(t, path, path3)
}

func TestStateFile_WriteRead_Roundtrip(t *testing.T) {
	host := "ssh://test-user@test-host.com"
	startedAt := time.Now().Add(-10 * time.Minute)
	
	writer, err := statefile.NewWriter(host, startedAt)
	require.NoError(t, err)
	defer writer.Delete()
	
	// Create test data
	forwards := []state.ForwardState{
		{
			ContainerID: "abc123",
			Port:        8080,
			Status:      "active",
			Reason:      "",
			CreatedAt:   time.Now().Add(-5 * time.Minute),
			UpdatedAt:   time.Now(),
		},
		{
			ContainerID: "def456",
			Port:        5432,
			Status:      "conflict",
			Reason:      "port already in use",
			CreatedAt:   time.Now().Add(-2 * time.Minute),
			UpdatedAt:   time.Now(),
		},
	}
	
	history := []state.HistoryEntry{
		{
			ContainerID: "xyz789",
			Port:        3000,
			StartedAt:   time.Now().Add(-30 * time.Minute),
			EndedAt:     time.Now().Add(-20 * time.Minute),
			EndReason:   "container stopped",
			FinalStatus: "active",
		},
	}
	
	// Write
	err = writer.Write(forwards, history)
	require.NoError(t, err)
	
	// Read back
	reader, err := statefile.NewReader(host)
	require.NoError(t, err)
	
	snapshot, err := reader.Read()
	require.NoError(t, err)
	
	// Verify
	assert.Equal(t, statefile.CurrentVersion, snapshot.Version)
	assert.Equal(t, host, snapshot.Host)
	assert.Equal(t, os.Getpid(), snapshot.PID)
	assert.Equal(t, 2, len(snapshot.Forwards))
	assert.Equal(t, 1, len(snapshot.History))
	
	// Verify forward data
	assert.Equal(t, "abc123", snapshot.Forwards[0].ContainerID)
	assert.Equal(t, 8080, snapshot.Forwards[0].Port)
	assert.Equal(t, "active", snapshot.Forwards[0].Status)
	
	assert.Equal(t, "def456", snapshot.Forwards[1].ContainerID)
	assert.Equal(t, 5432, snapshot.Forwards[1].Port)
	assert.Equal(t, "conflict", snapshot.Forwards[1].Status)
	assert.Equal(t, "port already in use", snapshot.Forwards[1].Reason)
	
	// Verify history data
	assert.Equal(t, "xyz789", snapshot.History[0].ContainerID)
	assert.Equal(t, 3000, snapshot.History[0].Port)
	assert.Equal(t, "container stopped", snapshot.History[0].EndReason)
}

func TestStateFile_IsStale(t *testing.T) {
	// Fresh state
	fresh := &statefile.StateFile{
		UpdatedAt: time.Now(),
	}
	assert.False(t, fresh.IsStale(), "Recent state should not be stale")
	
	// Stale state (15 seconds old, threshold is 10s)
	stale := &statefile.StateFile{
		UpdatedAt: time.Now().Add(-15 * time.Second),
	}
	assert.True(t, stale.IsStale(), "Old state should be stale")
	
	// Right at threshold
	threshold := &statefile.StateFile{
		UpdatedAt: time.Now().Add(-10 * time.Second),
	}
	assert.True(t, threshold.IsStale(), "State at threshold should be stale")
}

func TestWriter_Delete(t *testing.T) {
	host := "ssh://user@delete-test.com"
	
	writer, err := statefile.NewWriter(host, time.Now())
	require.NoError(t, err)
	
	// Write some data
	err = writer.Write([]state.ForwardState{}, []state.HistoryEntry{})
	require.NoError(t, err)
	
	// Verify file exists
	path, _ := statefile.GetStateFilePath(host)
	_, err = os.Stat(path)
	require.NoError(t, err, "File should exist")
	
	// Delete
	err = writer.Delete()
	require.NoError(t, err)
	
	// Verify file removed
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "File should be deleted")
}

func TestReader_MissingFile(t *testing.T) {
	host := "ssh://user@nonexistent.com"
	
	reader, err := statefile.NewReader(host)
	require.NoError(t, err)
	
	_, err = reader.Read()
	assert.True(t, os.IsNotExist(err), "Should return NotExist error for missing file")
}

func TestStateFile_CorruptJSON(t *testing.T) {
	host := "ssh://user@corrupt-test.com"
	path, err := statefile.GetStateFilePath(host)
	require.NoError(t, err)
	
	// Write corrupt JSON
	err = os.WriteFile(path, []byte("{invalid json"), 0600)
	require.NoError(t, err)
	defer os.Remove(path)
	
	reader, err := statefile.NewReader(host)
	require.NoError(t, err)
	
	_, err = reader.Read()
	assert.Error(t, err, "Should error on corrupt JSON")
	assert.Contains(t, err.Error(), "failed to decode")
}

func TestWriter_AtomicWrite(t *testing.T) {
	host := "ssh://user@atomic-test.com"
	
	writer, err := statefile.NewWriter(host, time.Now())
	require.NoError(t, err)
	defer writer.Delete()
	
	// Write data
	forwards := []state.ForwardState{
		{
			ContainerID: "test123",
			Port:        8080,
			Status:      "active",
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		},
	}
	
	err = writer.Write(forwards, []state.HistoryEntry{})
	require.NoError(t, err)
	
	// Verify no temp files remain
	path2, _ := statefile.GetStateFilePath(host)
	rdhpfDir := filepath.Dir(path2)
	entries, err := os.ReadDir(rdhpfDir)
	require.NoError(t, err)
	
	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), ".tmp", "No temp files should remain")
	}
	
	// Verify state file is valid JSON
	path, _ := statefile.GetStateFilePath(host)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	
	var snapshot statefile.StateFile
	err = json.Unmarshal(data, &snapshot)
	require.NoError(t, err, "State file should be valid JSON")
}