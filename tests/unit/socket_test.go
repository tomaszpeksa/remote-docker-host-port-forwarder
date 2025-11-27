package unit

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestSocketPath_Generation(t *testing.T) {
	host := "ssh://user@example.com"
	
	path, err := socket.GetSocketPath(host)
	require.NoError(t, err)
	
	// Should be in home directory
	homeDir, _ := os.UserHomeDir()
	assert.Contains(t, path, filepath.Join(homeDir, ".rdhpf"))
	
	// Should end with .sock
	assert.Contains(t, path, ".sock")
	
	// Path should be consistent for same host
	path2, err := socket.GetSocketPath(host)
	require.NoError(t, err)
	assert.Equal(t, path, path2)
	
	// Different host should give different path
	path3, err := socket.GetSocketPath("ssh://user@different.com")
	require.NoError(t, err)
	assert.NotEqual(t, path, path3)
}

func TestSocket_ServerLifecycle(t *testing.T) {
	host := "ssh://test-lifecycle@test.com"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st := state.NewState()
	hist := state.NewHistory()
	
	server, err := socket.NewServer(host, st, hist, time.Now(), logger)
	require.NoError(t, err)
	
	// Verify socket file exists after creation
	path, _ := socket.GetSocketPath(host)
	_, err = os.Stat(path)
	require.NoError(t, err, "Socket file should exist after NewServer")
	
	// Start server in background
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go server.Start(ctx)
	time.Sleep(50 * time.Millisecond) // Let server start
	
	// Close server
	err = server.Close()
	require.NoError(t, err)
	
	// Verify socket file is removed
	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err), "Socket file should be deleted")
}

func TestSocket_ClientQuery(t *testing.T) {
	host := "ssh://test-query@test.com"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	
	// Create state and history with test data
	st := state.NewState()
	hist := state.NewHistory()
	
	st.SetDesired("test123", []int{8080})
	st.MarkActive("test123", 8080)
	
	hist.Add(state.HistoryEntry{
		ContainerID: "old456",
		Port:        3000,
		StartedAt:   time.Now().Add(-30 * time.Minute),
		EndedAt:     time.Now().Add(-10 * time.Minute),
		EndReason:   "container stopped",
		FinalStatus: "active",
	})
	
	// Create and start server
	server, err := socket.NewServer(host, st, hist, time.Now(), logger)
	require.NoError(t, err)
	defer server.Close()
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go server.Start(ctx)
	time.Sleep(50 * time.Millisecond) // Let server start
	
	// Query from client
	client, err := socket.NewClient(host)
	require.NoError(t, err)
	
	result, err := client.GetStatus()
	require.NoError(t, err)
	
	// Verify results
	require.Equal(t, 1, len(result.Forwards))
	assert.Equal(t, "test123", result.Forwards[0].ContainerID)
	assert.Equal(t, 8080, result.Forwards[0].Port)
	assert.Equal(t, "active", result.Forwards[0].Status)
	
	require.Equal(t, 1, len(result.History))
	assert.Equal(t, "old456", result.History[0].ContainerID)
	assert.Equal(t, 3000, result.History[0].Port)
	assert.Equal(t, "container stopped", result.History[0].EndReason)
}

func TestSocket_ClientConnectionRefused(t *testing.T) {
	host := "ssh://nonexistent@test.com"
	
	client, err := socket.NewClient(host)
	require.NoError(t, err)
	
	// Try to query when no server is running
	_, err = client.GetStatus()
	assert.Error(t, err, "Should error when server is not running")
}

func TestSocket_MultipleClients(t *testing.T) {
	host := "ssh://test-multi@test.com"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	
	// Create state with test data
	st := state.NewState()
	hist := state.NewHistory()
	
	st.SetDesired("multi123", []int{8080})
	st.MarkActive("multi123", 8080)
	
	// Create and start server
	server, err := socket.NewServer(host, st, hist, time.Now(), logger)
	require.NoError(t, err)
	defer server.Close()
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go server.Start(ctx)
	time.Sleep(50 * time.Millisecond) // Let server start
	
	// Query from multiple clients concurrently
	results := make(chan error, 3)
	
	for i := 0; i < 3; i++ {
		go func() {
			client, err := socket.NewClient(host)
			if err != nil {
				results <- err
				return
			}
			
			result, err := client.GetStatus()
			if err != nil {
				results <- err
				return
			}
			
			if len(result.Forwards) != 1 || result.Forwards[0].ContainerID != "multi123" {
				results <- assert.AnError
				return
			}
			
			results <- nil
		}()
	}
	
	// Wait for all clients to complete
	for i := 0; i < 3; i++ {
		err := <-results
		assert.NoError(t, err, "All clients should succeed")
	}
}

func TestSocket_StateChangesReflected(t *testing.T) {
	host := "ssh://test-changes@test.com"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	
	// Create state with initial data
	st := state.NewState()
	hist := state.NewHistory()
	
	st.SetDesired("initial", []int{8080})
	st.MarkActive("initial", 8080)
	
	// Create and start server
	server, err := socket.NewServer(host, st, hist, time.Now(), logger)
	require.NoError(t, err)
	defer server.Close()
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go server.Start(ctx)
	time.Sleep(50 * time.Millisecond) // Let server start
	
	// First query
	client, err := socket.NewClient(host)
	require.NoError(t, err)
	
	result1, err := client.GetStatus()
	require.NoError(t, err)
	require.Equal(t, 1, len(result1.Forwards))
	assert.Equal(t, "initial", result1.Forwards[0].ContainerID)
	
	// Change state - the server returns actual state, and we need to explicitly remove the old forward
	// In real usage, the reconciler would handle this, but in tests we work with state directly
	st.SetDesired("updated", []int{9090})
	st.MarkActive("updated", 9090)
	
	// Second query should see both forwards (initial is still in actual state until reconciler removes it)
	result2, err := client.GetStatus()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result2.Forwards), 1, "Should have at least the updated forward")
	
	// Verify updated forward is present
	var foundUpdated bool
	for _, fwd := range result2.Forwards {
		if fwd.ContainerID == "updated" && fwd.Port == 9090 {
			foundUpdated = true
			break
		}
	}
	assert.True(t, foundUpdated, "Should find updated forward")
}

func TestSocket_EmptyState(t *testing.T) {
	host := "ssh://test-empty@test.com"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	
	// Create empty state
	st := state.NewState()
	hist := state.NewHistory()
	
	// Create and start server
	server, err := socket.NewServer(host, st, hist, time.Now(), logger)
	require.NoError(t, err)
	defer server.Close()
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go server.Start(ctx)
	time.Sleep(50 * time.Millisecond) // Let server start
	
	// Query
	client, err := socket.NewClient(host)
	require.NoError(t, err)
	
	result, err := client.GetStatus()
	require.NoError(t, err)
	
	// Verify empty results
	assert.Equal(t, 0, len(result.Forwards))
	assert.Equal(t, 0, len(result.History))
}

func TestSocket_HistoryReturned(t *testing.T) {
	host := "ssh://test-history@test.com"
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	
	// Create state and history
	st := state.NewState()
	hist := state.NewHistory()
	
	// Add multiple history entries
	hist.Add(state.HistoryEntry{
		ContainerID: "container1",
		Port:        8080,
		StartedAt:   time.Now().Add(-1 * time.Hour),
		EndedAt:     time.Now().Add(-30 * time.Minute),
		EndReason:   "container stopped",
		FinalStatus: "active",
	})
	
	hist.Add(state.HistoryEntry{
		ContainerID: "container2",
		Port:        9090,
		StartedAt:   time.Now().Add(-20 * time.Minute),
		EndedAt:     time.Now().Add(-5 * time.Minute),
		EndReason:   "port claimed by container3",
		FinalStatus: "conflict",
	})
	
	// Create and start server
	server, err := socket.NewServer(host, st, hist, time.Now(), logger)
	require.NoError(t, err)
	defer server.Close()
	
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	go server.Start(ctx)
	time.Sleep(50 * time.Millisecond) // Let server start
	
	// Query
	client, err := socket.NewClient(host)
	require.NoError(t, err)
	
	result, err := client.GetStatus()
	require.NoError(t, err)
	
	// Verify history
	assert.Equal(t, 2, len(result.History))
	
	// Find entries (order may vary)
	var container1Found, container2Found bool
	for _, entry := range result.History {
		if entry.ContainerID == "container1" {
			container1Found = true
			assert.Equal(t, 8080, entry.Port)
			assert.Equal(t, "container stopped", entry.EndReason)
		}
		if entry.ContainerID == "container2" {
			container2Found = true
			assert.Equal(t, 9090, entry.Port)
			assert.Equal(t, "port claimed by container3", entry.EndReason)
		}
	}
	
	assert.True(t, container1Found, "container1 should be in history")
	assert.True(t, container2Found, "container2 should be in history")
}