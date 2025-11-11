package unit

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
)

// safeBuffer wraps bytes.Buffer with mutex for thread-safe access
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestDockerEventsCommandUsesShell verifies that the docker events command
// is executed via sh -c to protect template syntax from shell expansion
func TestDockerEventsCommandUsesShell(t *testing.T) {
	// Create a dummy control path
	controlPath := "/tmp/test-control.sock"
	sshHost := "ssh://docker@testhost"

	// Create a thread-safe logger that outputs to a buffer
	var logOutput safeBuffer
	logger := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// Create event reader
	reader := docker.NewEventReader(sshHost, controlPath, logger)

	// Create a context that cancels immediately to prevent actual execution
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	// Start the stream (will fail because of invalid host, but that's ok)
	_, _ = reader.Stream(ctx)

	// Wait a bit for the goroutine to log
	time.Sleep(10 * time.Millisecond)

	// Check that the log output contains "sh -c"
	logStr := logOutput.String()
	if !strings.Contains(logStr, "sh -c") {
		t.Errorf("Expected command to use 'sh -c', but log output does not contain it:\n%s", logStr)
	}
}

// TestDockerEventsQuotesJSONTemplate verifies that the {{json .}} template
// is properly quoted in the docker command string
func TestDockerEventsQuotesJSONTemplate(t *testing.T) {
	controlPath := "/tmp/test-control.sock"
	sshHost := "ssh://docker@testhost"

	var logOutput safeBuffer
	logger := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	reader := docker.NewEventReader(sshHost, controlPath, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, _ = reader.Stream(ctx)
	time.Sleep(10 * time.Millisecond)

	logStr := logOutput.String()

	// The command should contain the quoted template: '{{json .}}'
	// This protects it from shell expansion
	if !strings.Contains(logStr, "'{{json .}}'") {
		t.Errorf("Expected command to contain quoted template '{{json .}}', but log output does not contain it:\n%s", logStr)
	}

	// Should NOT contain unquoted {{json .}} (without quotes)
	// We check for patterns that would indicate unquoted usage
	if strings.Contains(logStr, "--format {{json") || strings.Contains(logStr, "format {{json") {
		t.Errorf("Command appears to contain unquoted {{json template, which will fail:\n%s", logStr)
	}
}

// TestDockerCommandConstruction verifies the complete command structure
func TestDockerCommandConstruction(t *testing.T) {
	controlPath := "/tmp/test-control.sock"
	sshHost := "ssh://docker@testhost"

	var logOutput safeBuffer
	logger := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	reader := docker.NewEventReader(sshHost, controlPath, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, _ = reader.Stream(ctx)
	time.Sleep(10 * time.Millisecond)

	logStr := logOutput.String()

	// Verify essential command components
	requiredComponents := []string{
		"sh -c",                           // Uses shell
		"docker events",                   // Base command
		"'{{json .}}'",                    // Quoted template
		"--filter type=container",         // Container filter
		"--filter event=start",            // Start event filter
		"--filter event=die",              // Die event filter
		"--filter event=stop",             // Stop event filter
		"executing docker events command", // Log message
	}

	for _, component := range requiredComponents {
		if !strings.Contains(logStr, component) {
			t.Errorf("Command missing required component '%s':\n%s", component, logStr)
		}
	}
}

// TestDockerEventsCommandLogging verifies that comprehensive logging is present
func TestDockerEventsCommandLogging(t *testing.T) {
	controlPath := "/tmp/test-control.sock"
	sshHost := "ssh://docker@testhost"

	var logOutput safeBuffer
	logger := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	reader := docker.NewEventReader(sshHost, controlPath, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, _ = reader.Stream(ctx)
	time.Sleep(10 * time.Millisecond)

	logStr := logOutput.String()

	// Verify logging includes key information
	loggedInfo := []string{
		"executing docker events command", // Main log message
		"dockerCmd",                       // Docker command field
		"host",                            // Host field
		"command",                         // Full command field
	}

	for _, info := range loggedInfo {
		if !strings.Contains(logStr, info) {
			t.Errorf("Log output missing expected information '%s':\n%s", info, logStr)
		}
	}
}

// TestDockerEventsWithDifferentHosts verifies command construction works
// with different SSH host formats
func TestDockerEventsWithDifferentHosts(t *testing.T) {
	testCases := []struct {
		name         string
		sshHost      string
		expectedHost string
		expectedPort string
	}{
		{
			name:         "standard ssh URL",
			sshHost:      "ssh://docker@host1",
			expectedHost: "docker@host1",
			expectedPort: "",
		},
		{
			name:         "with port",
			sshHost:      "ssh://user@host2:2222",
			expectedHost: "user@host2",
			expectedPort: "2222",
		},
		{
			name:         "IP address",
			sshHost:      "ssh://root@192.168.1.100",
			expectedHost: "root@192.168.1.100",
			expectedPort: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			controlPath := "/tmp/test-control.sock"

			var logOutput safeBuffer
			logger := slog.New(slog.NewTextHandler(&logOutput, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			}))

			reader := docker.NewEventReader(tc.sshHost, controlPath, logger)

			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
			defer cancel()

			_, _ = reader.Stream(ctx)
			time.Sleep(10 * time.Millisecond)

			logStr := logOutput.String()

			// Verify the host appears in the log output
			expectedHostLog := "host=" + tc.expectedHost
			if !strings.Contains(logStr, expectedHostLog) {
				t.Errorf("Expected log to contain '%s', but it doesn't:\n%s", expectedHostLog, logStr)
			}

			// Verify port appears correctly in log (or is empty)
			if tc.expectedPort != "" {
				expectedPortLog := "port=" + tc.expectedPort
				if !strings.Contains(logStr, expectedPortLog) {
					t.Errorf("Expected log to contain '%s', but it doesn't:\n%s", expectedPortLog, logStr)
				}
			}

			// Verify sh -c is used regardless of host format
			if !strings.Contains(logStr, "sh -c") {
				t.Errorf("Expected command to use 'sh -c' for host %s:\n%s", tc.sshHost, logStr)
			}
		})
	}
}

func TestMain(m *testing.M) {
	// Run tests
	code := m.Run()
	os.Exit(code)
}
