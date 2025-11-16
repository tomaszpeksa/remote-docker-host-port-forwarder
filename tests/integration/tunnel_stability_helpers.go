package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// ============================================================================
// Data Structures
// ============================================================================

// TunnelStabilityStats tracks overall tunnel test statistics
type TunnelStabilityStats struct {
	TotalDuration    time.Duration
	ExpectedRequests int
	TotalSuccesses   int
	TotalFailures    int
	PerPortStats     map[int]*PortStats
}

// PortStats tracks per-port statistics
type PortStats struct {
	TotalRequests int
	SuccessCount  int
	FailureCount  int
	TotalLatency  time.Duration
}

// ReconnectionEvent records when SSH master reconnects
type ReconnectionEvent struct {
	Time        time.Time
	OldPID      int
	NewPID      int
	ElapsedTime time.Duration
}

// LoadStats tracks load test statistics
type LoadStats struct {
	Duration          time.Duration
	TotalRequests     int
	SuccessCount      int
	FailureCount      int
	RequestsPerSecond float64

	// Latency metrics
	MinLatency    time.Duration
	MaxLatency    time.Duration
	AvgLatency    time.Duration
	MedianLatency time.Duration
	P95Latency    time.Duration
	P99Latency    time.Duration

	Latencies []time.Duration // For percentile calculation
}

// PIDChange records when SSH master PID changes
type PIDChange struct {
	Time   time.Time
	OldPID int
	NewPID int
}

// ============================================================================
// Logger and Manager Setup
// ============================================================================

// createLoggerWithCapture creates a logger that writes to both stdout and a buffer
func createLoggerWithCapture(buf *bytes.Buffer, level string) *slog.Logger {
	// Multi-writer: stdout + buffer
	multiWriter := io.MultiWriter(os.Stdout, buf)

	var logLevel slog.Level
	switch level {
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	default:
		logLevel = slog.LevelInfo
	}

	handler := slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
		Level: logLevel,
	})

	return slog.New(handler)
}

// setupManagerWithLogger is like setupManager but accepts a custom logger
func setupManagerWithLogger(
	t *testing.T,
	ctx context.Context,
	sshHost string,
	logger *slog.Logger,
) *manager.Manager {
	t.Helper()

	// Create SSH master
	master, err := ssh.NewMaster(sshHost, logger)
	require.NoError(t, err, "Should create SSH master")

	err = master.Open(ctx)
	require.NoError(t, err, "Should open SSH ControlMaster")

	t.Cleanup(func() {
		master.Close()
	})

	// Get control path
	controlPath, err := ssh.DeriveControlPath(sshHost)
	require.NoError(t, err, "Should derive control path")

	// Create state
	st := state.NewState()

	// Create reconciler
	reconciler := reconcile.NewReconciler(st, logger)

	// Create event reader
	eventReader := docker.NewEventReader(sshHost, controlPath, logger)

	// Create config
	cfg := &config.Config{
		Host: sshHost,
	}

	// Create manager
	mgr := manager.NewManager(cfg, eventReader, reconciler, master, st, logger)

	// Start manager in background
	go func() {
		if err := mgr.Run(ctx); err != nil && ctx.Err() == nil {
			t.Logf("Manager stopped with error: %v", err)
		}
	}()

	return mgr
}

// ============================================================================
// SSH PID Detection and Monitoring
// ============================================================================

// getSSHMasterPID returns the PID of the SSH ControlMaster process
// Returns -1 if not found
func getSSHMasterPID(t *testing.T, sshHost string) int {
	t.Helper()

	controlPath, err := ssh.DeriveControlPath(sshHost)
	if err != nil {
		t.Logf("Failed to derive control path: %v", err)
		return -1
	}

	sshHostClean, port, err := ssh.ParseHost(sshHost)
	if err != nil {
		return -1
	}

	// Use SSH -O check to get PID
	args := []string{"-S", controlPath, "-O", "check"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshHostClean)

	keyPath := os.Getenv("SSH_TEST_KEY_PATH")
	if keyPath != "" {
		args = append([]string{"-i", keyPath}, args...)
	}

	cmd := exec.Command("ssh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return -1
	}

	// Parse "Master running (pid=12345)"
	re := regexp.MustCompile(`pid=(\d+)`)
	matches := re.FindStringSubmatch(string(output))
	if len(matches) < 2 {
		return -1
	}

	pid, _ := strconv.Atoi(matches[1])
	return pid
}

// monitorSSHPIDChanges monitors SSH ControlMaster PID during a test
func monitorSSHPIDChanges(
	t *testing.T,
	sshHost string,
	initialPID int,
	duration time.Duration,
) []PIDChange {
	t.Helper()

	changes := make([]PIDChange, 0)
	currentPID := initialPID

	startTime := time.Now()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for time.Since(startTime) < duration {
		<-ticker.C

		newPID := getSSHMasterPID(t, sshHost)
		if newPID != -1 && newPID != currentPID {
			changes = append(changes, PIDChange{
				Time:   time.Now(),
				OldPID: currentPID,
				NewPID: newPID,
			})
			currentPID = newPID
		}
	}

	return changes
}

// ============================================================================
// Log Verification
// ============================================================================

// verifyNoReconnectionWarnings checks logs for reconnection indicators
// Returns list of issues found
func verifyNoReconnectionWarnings(t *testing.T, logOutput string) []string {
	t.Helper()

	issues := make([]string, 0)

	// Patterns that indicate reconnections
	patterns := map[string]string{
		"Permanently added":                      "New SSH connection (not using ControlMaster)",
		"event stream failed after":              "Event stream failures",
		"SSH ControlMaster is dead, recreating": "ControlMaster recreation",
		"consecutive_failures=[3-9]":             "Multiple consecutive failures (3+)",
		"consecutive_failures=[1-9][0-9]":        "High failure count (10+)",
	}

	for pattern, description := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllString(logOutput, -1)

		if len(matches) > 0 {
			issues = append(issues, fmt.Sprintf("%s: found %d instances",
				description, len(matches)))
		}
	}

	// Check for single stream start (stability indicator)
	streamStarts := strings.Count(logOutput, "DIAGNOSTIC: Starting Docker events stream")
	if streamStarts > 1 {
		issues = append(issues, fmt.Sprintf(
			"Stream restarted %d times (expected 1)", streamStarts))
	}

	// Verify we DO have health confirmations (positive check)
	healthConfirms := strings.Count(logOutput, "SSH ControlMaster verified healthy")
	if healthConfirms == 0 {
		issues = append(issues,
			"No health check confirmations found (expected multiple)")
	}

	return issues
}

// ============================================================================
// Load Generation
// ============================================================================

// generateLoad creates concurrent workers making HTTP requests
func generateLoad(
	ctx context.Context,
	workerCount int,
	requestInterval time.Duration,
	targetPort int,
	duration time.Duration,
) *LoadStats {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		stats = &LoadStats{
			MinLatency: time.Hour, // Will be updated
			Latencies:  make([]time.Duration, 0, 10000),
		}
	)

	// Create HTTP client with timeouts
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: workerCount * 2,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	startTime := time.Now()
	testCtx, cancel := context.WithTimeout(ctx, duration+10*time.Second)
	defer cancel()

	// Launch workers
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			ticker := time.NewTicker(requestInterval)
			defer ticker.Stop()

			for {
				select {
				case <-testCtx.Done():
					return

				case <-ticker.C:
					if time.Since(startTime) >= duration {
						return
					}

					// Make HTTP request
					reqStart := time.Now()
					resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", targetPort))
					latency := time.Since(reqStart)

					// Record stats
					mu.Lock()
					stats.TotalRequests++

					if err == nil && resp.StatusCode == 200 {
						stats.SuccessCount++
						resp.Body.Close()

						// Update latency stats
						stats.Latencies = append(stats.Latencies, latency)
						if latency < stats.MinLatency {
							stats.MinLatency = latency
						}
						if latency > stats.MaxLatency {
							stats.MaxLatency = latency
						}
					} else {
						stats.FailureCount++
						if resp != nil {
							resp.Body.Close()
						}
					}
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()

	// Calculate final statistics
	stats.Duration = time.Since(startTime)
	stats.RequestsPerSecond = float64(stats.TotalRequests) / stats.Duration.Seconds()

	// Calculate latency percentiles
	if len(stats.Latencies) > 0 {
		sort.Slice(stats.Latencies, func(i, j int) bool {
			return stats.Latencies[i] < stats.Latencies[j]
		})

		var totalLatency time.Duration
		for _, l := range stats.Latencies {
			totalLatency += l
		}
		stats.AvgLatency = totalLatency / time.Duration(len(stats.Latencies))

		stats.MedianLatency = stats.Latencies[len(stats.Latencies)/2]
		stats.P95Latency = stats.Latencies[int(float64(len(stats.Latencies))*0.95)]
		stats.P99Latency = stats.Latencies[int(float64(len(stats.Latencies))*0.99)]
	}

	return stats
}