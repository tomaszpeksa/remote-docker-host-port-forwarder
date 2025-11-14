package integration

import (
	"context"
	"testing"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// TestDockerEventsStreamPersistence verifies that the docker events stream
// stays open and doesn't close immediately. This test specifically catches
// the bug where SSH ControlMaster causes streams to close prematurely.
//
// Reported Bug (v0.1.4):
//   - Stream starts successfully
//   - Stream closes after ~40ms
//   - Application enters restart loop
//   - No containers detected, no port forwards created
//
// This test FAILS if:
//   - Stream closes before 5 seconds
//   - Error channel closes unexpectedly
//
// This test PASSES if:
//   - Stream stays open for at least 5 seconds
//   - Stream can be cancelled cleanly
func TestDockerEventsStreamPersistence(t *testing.T) {
	sshHost := getTestSSHHost(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Setup SSH ControlMaster
	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(sshHost, logger)
	if err != nil {
		t.Fatalf("Failed to create SSH master: %v", err)
	}

	err = master.Open(ctx)
	if err != nil {
		t.Fatalf("Failed to open SSH ControlMaster: %v", err)
	}
	defer master.Close()

	// Get control path
	controlPath, err := ssh.DeriveControlPath(sshHost)
	if err != nil {
		t.Fatalf("Failed to derive control path: %v", err)
	}

	// Start docker events stream
	t.Log("Starting docker events stream via SSH ControlMaster...")
	reader := docker.NewEventReader(sshHost, controlPath, logger)
	events, errs := reader.Stream(ctx)

	// CRITICAL TEST: Stream should NOT close immediately
	// The bug this catches: stream closes after ~40ms
	// Expected behavior: stream stays open for at least 5 seconds

	streamStartTime := time.Now()
	t.Logf("Stream started at %v, monitoring for early closure...", streamStartTime)

	select {
	case <-events:
		elapsed := time.Since(streamStartTime)
		t.Fatalf("❌ BUG DETECTED: Event stream channel closed after %v (expected to stay open)\n"+
			"This indicates the docker events process exited prematurely.\n"+
			"Likely cause: SSH ControlMaster not keeping stream open or shell expansion issue.\n"+
			"See: https://github.com/tomaszpeksa/rdhpf/issues/<bug-tracker>", elapsed)

	case err := <-errs:
		elapsed := time.Since(streamStartTime)
		if err == nil {
			t.Fatalf("❌ BUG DETECTED: Error channel closed after %v (no error reported)\n"+
				"This indicates the docker events process exited without error but prematurely.", elapsed)
		}
		t.Fatalf("❌ BUG DETECTED: Error channel received error after %v: %v\n"+
			"Stream should not error out this quickly in normal operation.", elapsed, err)

	case <-time.After(5 * time.Second):
		elapsed := time.Since(streamStartTime)
		t.Logf("✅ SUCCESS: Stream stayed open for %v (correct behavior)", elapsed)
		t.Log("Stream is behaving correctly - docker events process is persistent")
	}

	// Note: We don't test clean cancellation here because it depends on SSH process
	// termination timing which can vary. The critical behavior (stream persistence)
	// is already validated above. Clean shutdown is tested separately.
}

// TestDockerEventsStreamReceivesEvents verifies that the stream not only
// stays open but also successfully receives and processes events when
// containers are started/stopped.
func TestDockerEventsStreamReceivesEvents(t *testing.T) {
	sshHost := getTestSSHHost(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Setup SSH ControlMaster
	logger := logging.NewLogger("debug")
	master, err := ssh.NewMaster(sshHost, logger)
	if err != nil {
		t.Fatalf("Failed to create SSH master: %v", err)
	}

	err = master.Open(ctx)
	if err != nil {
		t.Fatalf("Failed to open SSH ControlMaster: %v", err)
	}
	defer master.Close()

	// Get control path
	controlPath, err := ssh.DeriveControlPath(sshHost)
	if err != nil {
		t.Fatalf("Failed to derive control path: %v", err)
	}

	// Start docker events stream
	t.Log("Starting docker events stream...")
	reader := docker.NewEventReader(sshHost, controlPath, logger)
	events, errs := reader.Stream(ctx)

	// Start a test container to generate events
	t.Log("Starting test container to generate events...")
	containerID, cleanup := startDockerContainer(t, sshHost, "alpine:latest", map[int]int{})
	defer cleanup()

	// Wait for and verify we receive events
	t.Log("Waiting for container events...")
	receivedEvent := false

	eventTimeout := time.After(10 * time.Second)
	for {
		select {
		case event := <-events:
			if event.ContainerID == containerID[:12] || event.ContainerID == containerID {
				t.Logf("✅ Received event for container %s: %s", containerID[:12], event.Type)
				receivedEvent = true
				// Continue listening for more events briefly
				time.Sleep(1 * time.Second)
				goto done
			}
			t.Logf("Received event for different container: %s %s", event.ContainerID, event.Type)

		case err := <-errs:
			if err != nil {
				t.Fatalf("Stream error: %v", err)
			}
			t.Fatal("Error channel closed unexpectedly")

		case <-eventTimeout:
			if !receivedEvent {
				t.Fatal("❌ Timeout: No events received within 10 seconds")
			}
			goto done
		}
	}

done:
	if !receivedEvent {
		t.Fatal("❌ Failed to receive any events for test container")
	}

	t.Log("✅ Stream successfully receives and processes events")
}
