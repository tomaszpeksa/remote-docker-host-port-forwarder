package manager

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

// dockerPingRunner executes local docker run commands for health pings
type dockerPingRunner interface {
	Ping(ctx context.Context) error
}

// localDockerPingRunner runs docker commands locally (using DOCKER_HOST config)
type localDockerPingRunner struct {
	logger *slog.Logger
}

func newLocalDockerPingRunner(logger *slog.Logger) dockerPingRunner {
	return &localDockerPingRunner{logger: logger}
}

func (r *localDockerPingRunner) Ping(ctx context.Context) error {
	name := fmt.Sprintf("rdhpf-ping-%d", time.Now().UnixNano())
	args := []string{
		"run", "--rm",
		"--name", name,
		"--label", "rdhpf.health-check=true",
		"alpine:latest",
		"echo", "works",
	}

	// #nosec G204 - docker command with controlled args
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.logger.Warn("event health ping docker run failed",
			"error", err.Error(),
			"output", string(out))
		return fmt.Errorf("event health ping docker run failed: %w", err)
	}

	r.logger.Debug("event health ping docker run succeeded",
		"output", strings.TrimSpace(string(out)))
	return nil
}

// eventWatchdog monitors Docker event stream health via periodic pings
type eventWatchdog struct {
	now           func() time.Time
	dockerPing    dockerPingRunner
	idleThreshold time.Duration // 30s - when to start pinging
	fatalAfter    time.Duration // 60s - when to consider stream dead

	mu        sync.RWMutex
	lastEvent time.Time
}

func newEventWatchdog(now func() time.Time, ping dockerPingRunner) *eventWatchdog {
	t := now()
	return &eventWatchdog{
		now:           now,
		dockerPing:    ping,
		idleThreshold: 30 * time.Second,
		fatalAfter:    60 * time.Second,
		lastEvent:     t,
	}
}

// OnEvent should be called whenever any Docker event is processed
func (w *eventWatchdog) OnEvent() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastEvent = w.now()
}

// Tick should be called periodically (~10s) to check event stream health
// Returns a fatal error if no events have been seen for >= fatalAfter duration
func (w *eventWatchdog) Tick(ctx context.Context) error {
	w.mu.RLock()
	last := w.lastEvent
	w.mu.RUnlock()

	now := w.now()
	dt := now.Sub(last)

	switch {
	case dt < w.idleThreshold:
		// Recent events; stream is healthy
		return nil

	case dt < w.fatalAfter:
		// Idle window: try to generate an event via ping
		_ = w.dockerPing.Ping(ctx) // Errors are logged inside Ping; don't make them fatal
		return nil

	default:
		// Too long without any event (including pings)
		return fmt.Errorf("event stream unhealthy: no events for %s", dt.Round(time.Second))
	}
}

// Manager orchestrates Docker event handling and port forward reconciliation.
type Manager struct {
	cfg         *config.Config
	eventReader *docker.EventReader
	reconciler  *reconcile.Reconciler
	sshMaster   *ssh.Master
	state       *state.State
	logger      *slog.Logger

	// Event stream health monitoring
	watchdog   *eventWatchdog
	now        func() time.Time
	dockerPing dockerPingRunner

	// Performance metrics
	metrics performanceMetrics

	// State persistence and IPC
	history      *state.History
	stateWriter  *statefile.Writer
	socketServer *socket.Server
	startedAt    time.Time
}

// performanceMetrics tracks performance data
type performanceMetrics struct {
	mu                   sync.RWMutex
	eventProcessingTimes []time.Duration // Rolling window of last 100 events
	reconciliationTimes  []time.Duration // Rolling window of last 100 reconciliations
	sshCommandTimes      []time.Duration // Rolling window of last 100 commands
	totalEventsProcessed int64
	totalReconciliations int64
	startTime            time.Time
}

func (m *performanceMetrics) recordEventProcessing(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventProcessingTimes = append(m.eventProcessingTimes, duration)
	if len(m.eventProcessingTimes) > 100 {
		m.eventProcessingTimes = m.eventProcessingTimes[1:]
	}
	m.totalEventsProcessed++
}

func (m *performanceMetrics) recordReconciliation(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reconciliationTimes = append(m.reconciliationTimes, duration)
	if len(m.reconciliationTimes) > 100 {
		m.reconciliationTimes = m.reconciliationTimes[1:]
	}
	m.totalReconciliations++
}

func (m *performanceMetrics) recordSSHCommand(duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sshCommandTimes = append(m.sshCommandTimes, duration)
	if len(m.sshCommandTimes) > 100 {
		m.sshCommandTimes = m.sshCommandTimes[1:]
	}
}

func (m *performanceMetrics) getStats() (avgEventTime, avgReconcileTime, avgSSHTime time.Duration, uptime time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.eventProcessingTimes) > 0 {
		var total time.Duration
		for _, d := range m.eventProcessingTimes {
			total += d
		}
		avgEventTime = total / time.Duration(len(m.eventProcessingTimes))
	}

	if len(m.reconciliationTimes) > 0 {
		var total time.Duration
		for _, d := range m.reconciliationTimes {
			total += d
		}
		avgReconcileTime = total / time.Duration(len(m.reconciliationTimes))
	}

	if len(m.sshCommandTimes) > 0 {
		var total time.Duration
		for _, d := range m.sshCommandTimes {
			total += d
		}
		avgSSHTime = total / time.Duration(len(m.sshCommandTimes))
	}

	uptime = time.Since(m.startTime)
	return
}

// NewManager creates a new Manager instance.
//
// Parameters:
//   - cfg: Application configuration
//   - eventReader: Docker event stream reader
//   - reconciler: Reconciler for computing and applying actions
//   - sshMaster: SSH ControlMaster connection
//   - state: Shared state manager
//   - history: History manager for tracking removed forwards
//   - logger: Structured logger for operation logging
//
// Example usage:
//
//	manager := NewManager(cfg, eventReader, reconciler, sshMaster, state, history, logger)
//	if err := manager.Run(ctx); err != nil {
//	    log.Fatal(err)
//	}
func NewManager(
	cfg *config.Config,
	eventReader *docker.EventReader,
	reconciler *reconcile.Reconciler,
	sshMaster *ssh.Master,
	state *state.State,
	history *state.History,
	logger *slog.Logger,
) *Manager {
	now := time.Now
	dockerPing := newLocalDockerPingRunner(logger)
	startedAt := time.Now()

	return &Manager{
		cfg:         cfg,
		eventReader: eventReader,
		reconciler:  reconciler,
		sshMaster:   sshMaster,
		state:       state,
		logger:      logger,
		now:         now,
		dockerPing:  dockerPing,
		watchdog:    newEventWatchdog(now, dockerPing),
		metrics: performanceMetrics{
			startTime: startedAt,
		},
		history:   history,
		startedAt: startedAt,
	}
}

// Run starts the manager's main event loop.
//
// It performs these operations:
//
//  1. Performs startup reconciliation (syncs with currently running containers)
//  2. Subscribes to Docker events
//  3. Handles start/die/stop events as they arrive
//  4. Automatically restarts event stream on failures with exponential backoff
//  5. Continues until context is canceled
//
// Example usage:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	if err := manager.Run(ctx); err != nil {
//	    log.Fatal(err)
//	}
func (m *Manager) Run(ctx context.Context) error {
	m.logger.Info("manager starting")

	// Initialize state writer
	var err error
	m.stateWriter, err = statefile.NewWriter(m.cfg.Host, m.startedAt)
	if err != nil {
		return fmt.Errorf("failed to create state writer: %w", err)
	}
	defer m.stateWriter.Close()

	// Initialize socket server
	m.socketServer, err = socket.NewServer(m.cfg.Host, m.state, m.history, m.startedAt, m.logger)
	if err != nil {
		m.logger.Warn("failed to create socket server, status will use file only", "error", err)
	} else {
		go func() {
			if err := m.socketServer.Start(ctx); err != nil && ctx.Err() == nil {
				m.logger.Warn("socket server error", "error", err)
			}
		}()
		defer func() {
			if err := m.socketServer.Close(); err != nil {
				m.logger.Warn("failed to close socket server", "error", err)
			}
		}()
		m.logger.Info("socket server started")
	}

	// Start background state writer
	go m.startStateWriter(ctx)

	// Set up SSH master recovery callback to trigger reconciliation
	m.sshMaster.SetRecoveryCallback(func() {
		m.logger.Info("SSH connection recovered, reconciling state")
		if err := m.triggerReconcile(ctx); err != nil {
			m.logger.Warn("reconciliation after SSH recovery failed",
				"error", err.Error())
		}
	})

	// Start SSH health monitor (check every 15 seconds for faster failure detection)
	m.sshMaster.StartHealthMonitor(ctx, 15*time.Second)
	m.logger.Info("SSH health monitoring started",
		"check_interval", "15s",
		"control_path", m.sshMaster.ControlPath())

	// Start performance metrics logger (log every 5 minutes)
	go m.logPerformanceMetrics(ctx, 5*time.Minute)

	// Start event stream watchdog (checks every 10s, pings after 30s idle, fatal after 60s)
	fatalCh := make(chan error, 1)
	go m.startEventWatchdogLoop(ctx, fatalCh)
	m.logger.Info("event stream watchdog started",
		"tick_interval", "10s",
		"idle_threshold", "30s",
		"fatal_after", "60s")

	// Perform startup reconciliation
	if err := m.reconcileStartup(ctx); err != nil {
		return fmt.Errorf("startup reconciliation failed: %w", err)
	}

	// Event stream restart logic with exponential backoff
	// Spec: 1s, 2s, 4s, 8s, max 30s; max 10 consecutive failures
	maxConsecutiveFailures := 10
	consecutiveFailures := 0
	baseDelay := 1 * time.Second

	for {
		// Check for watchdog fatal error
		select {
		case err := <-fatalCh:
			if err != nil {
				m.logger.Error("event stream watchdog detected failure, shutting down",
					"error", err.Error())
				m.cleanupAllForwards(context.Background())
				return err
			}
		default:
			// Continue with normal loop
		}

		// Check if context is canceled before starting/restarting
		if ctx.Err() != nil {
			m.logger.Info("manager stopping due to context cancellation")
			m.cleanupAllForwards(context.Background())
			return nil
		}

		// CRITICAL: Ensure SSH ControlMaster is alive before starting event stream
		// This prevents creating non-multiplexed SSH sessions that die immediately
		m.logger.Info("Ensuring SSH ControlMaster is alive before starting event stream",
			"attempt", consecutiveFailures+1)

		if err := m.sshMaster.EnsureAlive(ctx); err != nil {
			m.logger.Error("Failed to ensure SSH ControlMaster is alive",
				"error", err.Error(),
				"consecutive_failures", consecutiveFailures)
			consecutiveFailures++

			// Check if we've exceeded max failures
			if consecutiveFailures >= maxConsecutiveFailures {
				return fmt.Errorf("SSH ControlMaster failed after %d consecutive failures", maxConsecutiveFailures)
			}

			// Calculate backoff delay
			backoffFactor := consecutiveFailures - 1
			if backoffFactor < 0 {
				backoffFactor = 0
			}
			if backoffFactor > 31 {
				backoffFactor = 31
			}
			// #nosec G115 - backoffFactor is bounds-checked above
			delay := baseDelay * time.Duration(1<<uint(backoffFactor))
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}

			m.logger.Warn("SSH ControlMaster unavailable, retrying after backoff",
				"consecutive_failures", consecutiveFailures,
				"backoff_delay", delay,
				"max_failures", maxConsecutiveFailures)

			// Wait before retry
			select {
			case <-time.After(delay):
				continue // Retry from top of loop
			case <-ctx.Done():
				return nil
			}
		}

		// DIAGNOSTIC: Pre-flight checks before starting stream
		controlPath, err := ssh.DeriveControlPath(m.cfg.Host)
		if err == nil {
			m.logger.Info("DIAGNOSTIC: SSH ControlMaster verified healthy before stream start",
				"control_path", controlPath,
				"consecutive_failures", consecutiveFailures)

			// Test Docker daemon connectivity with a simple command
			if err := m.validateDockerConnectivity(ctx, controlPath); err != nil {
				m.logger.Warn("DIAGNOSTIC: Docker daemon connectivity test failed",
					"error", err.Error())
			} else {
				m.logger.Info("DIAGNOSTIC: Docker daemon connectivity validated")
			}
		}

		// Start event stream with healthy ControlMaster
		streamStartTime := time.Now()
		m.logger.Info("DIAGNOSTIC: Starting Docker events stream",
			"attempt_number", consecutiveFailures+1,
			"max_attempts", maxConsecutiveFailures,
			"timestamp", streamStartTime.Format(time.RFC3339))

		events, errs := m.eventReader.Stream(ctx)

		if consecutiveFailures > 0 {
			m.logger.Warn("event stream restarted",
				"attempt", consecutiveFailures+1,
				"max_failures", maxConsecutiveFailures)
		} else {
			m.logger.Info("manager event loop started")
		}

		// Run event loop until stream closes or errors
		streamClosed := m.runEventLoop(ctx, events, errs)
		streamDuration := time.Since(streamStartTime)

		// DIAGNOSTIC: Log stream end with timing
		m.logger.Info("DIAGNOSTIC: Docker events stream ended",
			"duration", streamDuration.String(),
			"duration_ms", streamDuration.Milliseconds(),
			"clean_close", streamClosed,
			"consecutive_failures", consecutiveFailures)

		// If stream closed cleanly due to context cancellation, cleanup and exit
		if ctx.Err() != nil {
			m.logger.Info("manager stopping due to context cancellation")
			m.cleanupAllForwards(context.Background())
			return nil
		}

		// Stream closed unexpectedly
		if !streamClosed {
			consecutiveFailures++

			// Check if we've exceeded max failures
			if consecutiveFailures >= maxConsecutiveFailures {
				return fmt.Errorf("event stream failed after %d consecutive failures", maxConsecutiveFailures)
			}

			// Calculate backoff delay: 1s, 2s, 4s, 8s, ..., max 30s
			// Safely convert to uint to prevent overflow
			backoffFactor := consecutiveFailures - 1
			if backoffFactor < 0 {
				backoffFactor = 0
			}
			if backoffFactor > 31 {
				backoffFactor = 31 // Prevent overflow in bit shift
			}
			// #nosec G115 - backoffFactor is bounds-checked above (0-31 range)
			delay := baseDelay * time.Duration(1<<uint(backoffFactor))
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}

			// DIAGNOSTIC: Log detailed failure context
			m.logger.Warn("event stream error, restarting after backoff",
				"consecutive_failures", consecutiveFailures,
				"backoff_delay", delay,
				"max_failures", maxConsecutiveFailures,
				"stream_duration_ms", streamDuration.Milliseconds(),
				"timestamp", time.Now().Format(time.RFC3339))

			// Wait before retry
			select {
			case <-time.After(delay):
				// After restart, trigger reconciliation to ensure state is correct
				if err := m.triggerReconcile(ctx); err != nil {
					m.logger.Warn("reconciliation after stream restart failed",
						"error", err.Error())
				} else {
					m.logger.Info("reconciliation after stream restart completed")
				}
			case <-ctx.Done():
				return nil
			}
		} else {
			// Stream closed cleanly, reset failure count
			if consecutiveFailures > 0 {
				m.logger.Info("event stream recovered",
					"previous_failures", consecutiveFailures)
				consecutiveFailures = 0
			}
		}
	}
}

// startEventWatchdogLoop runs the event stream health watchdog
func (m *Manager) startEventWatchdogLoop(ctx context.Context, fatalCh chan<- error) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if err := m.watchdog.Tick(ctx); err != nil {
				m.logger.Error("event stream watchdog tick failed",
					"error", err.Error())
				select {
				case fatalCh <- err:
				default:
					// Channel already has a fatal error; drop this one
				}
				return
			}
		}
	}
}

// runEventLoop processes events until the stream closes or errors.
// Implements debouncing: instead of reconciling on every event, it batches
// events together and reconciles 200ms after the last event received.
//
// Returns true if stream closed cleanly, false otherwise
func (m *Manager) runEventLoop(ctx context.Context, events <-chan docker.Event, errs <-chan error) bool {
	// Debouncing timer - reconcile 200ms after last event
	var debounceTimer *time.Timer
	var eventCount int

	// Helper to reset/create timer
	resetDebounceTimer := func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.NewTimer(200 * time.Millisecond)
	}

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return true

		case err, ok := <-errs:
			if !ok {
				// Error channel closed, stream ended
				m.logger.Info("event error channel closed")
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				return true
			}
			m.logger.Error("event stream error", "error", err.Error())
			// Stream error occurred, will trigger restart
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return false

		case event, ok := <-events:
			if !ok {
				// Event channel closed, stream ended
				m.logger.Info("event channel closed")
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				return true
			}

			// Handle event based on type
			switch event.Type {
			case "start":
				if err := m.handleStartEvent(ctx, event); err != nil {
					m.logger.Error("failed to handle start event",
						"containerID", event.ContainerID[:12],
						"error", err.Error())
				} else {
					eventCount++
					resetDebounceTimer()
					// Notify watchdog that we received an event
					m.watchdog.OnEvent()
				}

			case "die", "stop":
				if err := m.handleStopEvent(ctx, event); err != nil {
					m.logger.Error("failed to handle stop event",
						"containerID", event.ContainerID[:12],
						"error", err.Error())
				} else {
					eventCount++
					resetDebounceTimer()
					// Notify watchdog that we received an event
					m.watchdog.OnEvent()
				}

			default:
				m.logger.Warn("unexpected event type",
					"type", event.Type,
					"containerID", event.ContainerID[:12])
			}

		case <-func() <-chan time.Time {
			if debounceTimer != nil {
				return debounceTimer.C
			}
			// Return a channel that never fires if timer not set
			return make(<-chan time.Time)
		}():
			// Timer fired - reconcile now
			if eventCount > 0 {
				m.logger.Info("debounce timer fired, reconciling",
					"batched_events", eventCount)

				if err := m.triggerReconcile(ctx); err != nil {
					m.logger.Error("debounced reconciliation failed",
						"error", err.Error())
				}

				eventCount = 0
				debounceTimer = nil
			}
		}
	}
}

// handleStartEvent processes a container start event.
//
// Steps:
//  1. Inspect the container to get its published ports
//  2. Update desired state with the ports
//  3. Trigger debounced reconciliation
func (m *Manager) handleStartEvent(ctx context.Context, event docker.Event) error {
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		m.metrics.recordEventProcessing(duration)
		m.logger.Debug("event processing time",
			"event", "start",
			"containerID", event.ContainerID[:12],
			"duration_ms", duration.Milliseconds())
	}()

	m.logger.Info("handling container start",
		"containerID", event.ContainerID[:12])

	// Get control path for docker inspect
	controlPath, err := ssh.DeriveControlPath(m.cfg.Host)
	if err != nil {
		return fmt.Errorf("failed to derive control path: %w", err)
	}

	// Inspect container to get published ports
	cmdStart := time.Now()
	ports, err := docker.InspectPorts(ctx, m.cfg.Host, controlPath, event.ContainerID)
	m.metrics.recordSSHCommand(time.Since(cmdStart))

	if err != nil {
		return fmt.Errorf("failed to inspect container ports: %w", err)
	}

	m.logger.Info("container ports discovered",
		"containerID", event.ContainerID[:12],
		"ports", ports)

	// Update desired state
	m.state.SetDesired(event.ContainerID, ports)

	// Note: We don't reconcile immediately anymore
	// The runEventLoop handles debounced reconciliation

	return nil
}

// handleStopEvent processes a container stop or die event.
//
// Steps:
//  1. Clear desired state for the container (set to empty ports)
//  2. Trigger debounced reconciliation
func (m *Manager) handleStopEvent(ctx context.Context, event docker.Event) error {
	m.logger.Info("handling container stop",
		"containerID", event.ContainerID[:12])

	// Clear desired state (empty ports = no forwards wanted)
	m.state.SetDesired(event.ContainerID, []int{})

	// Note: We don't reconcile immediately anymore
	// The runEventLoop handles debounced reconciliation

	return nil
}

// triggerReconcile performs a reconciliation cycle.
//
// Steps:
//  1. Compute diff between desired and actual state
//  2. Apply the computed actions
func (m *Manager) triggerReconcile(ctx context.Context) error {
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		m.metrics.recordReconciliation(duration)
		m.logger.Debug("reconciliation time",
			"duration_ms", duration.Milliseconds())
	}()

	toAdd, toRemove := m.reconciler.Diff()

	if len(toAdd) == 0 && len(toRemove) == 0 {
		m.logger.Debug("reconciliation: no actions needed")
		return nil
	}

	// Combine actions for Apply
	actions := append(toRemove, toAdd...)

	m.logger.Info("reconciliation starting",
		"actions", len(actions))

	if err := m.reconciler.Apply(ctx, m.sshMaster, m.cfg.Host, actions); err != nil {
		return fmt.Errorf("apply failed: %w", err)
	}

	m.logger.Info("reconciliation complete")
	return nil
}

// logPerformanceMetrics periodically logs performance metrics summary
func (m *Manager) logPerformanceMetrics(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			avgEvent, avgReconcile, avgSSH, uptime := m.metrics.getStats()

			// Count active forwards and conflicts
			actualStates := m.state.GetActual()
			activeCount := 0
			conflictCount := 0
			for _, fs := range actualStates {
				switch fs.Status {
				case "active":
					activeCount++
				case "conflict":
					conflictCount++
				}
			}

			m.logger.Info("Performance summary",
				"avg_event_latency_ms", avgEvent.Milliseconds(),
				"avg_reconcile_ms", avgReconcile.Milliseconds(),
				"avg_ssh_cmd_ms", avgSSH.Milliseconds(),
				"active_forwards", activeCount,
				"conflicts", conflictCount,
				"total_events", m.metrics.totalEventsProcessed,
				"total_reconciliations", m.metrics.totalReconciliations,
				"uptime", uptime.Round(time.Second).String())
		}
	}
}

// reconcileStartup synchronizes with currently running containers on startup.
//
// This ensures that if the tool is started while containers are already running,
// it will establish forwards for them.
//
// Steps:
//  1. Execute `docker ps --format '{{.ID}}'` via SSH to get running container IDs
//  2. For each container, inspect its ports
//  3. Update desired state
//  4. Perform reconciliation
func (m *Manager) reconcileStartup(ctx context.Context) error {
	m.logger.Info("performing startup reconciliation")

	// Get control path for SSH commands
	controlPath, err := ssh.DeriveControlPath(m.cfg.Host)
	if err != nil {
		return fmt.Errorf("failed to derive control path: %w", err)
	}

	// Parse host and port from SSH URL
	sshHost, port, err := ssh.ParseHost(m.cfg.Host)
	if err != nil {
		return fmt.Errorf("failed to parse SSH host: %w", err)
	}

	// Get list of running containers
	// Build the docker command as a single quoted string to protect {{.ID}} from shell expansion
	dockerCmd := "docker ps --format '{{.ID}}'"

	// Build SSH command args
	// Important: sh -c and the docker command must be passed as a single argument to SSH
	remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)
	args := []string{"-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshHost, remoteCmd)

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to list running containers: %w", err)
	}

	// Parse container IDs
	containerIDs := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			containerIDs = append(containerIDs, line)
		}
	}

	m.logger.Info("found running containers",
		"count", len(containerIDs))

	// Inspect each container and update desired state
	for _, containerID := range containerIDs {
		// Check if container has test-infrastructure label (skip it)
		if m.hasTestInfrastructureLabel(ctx, controlPath, containerID) {
			m.logger.Debug("skipping test infrastructure container",
				"containerID", containerID[:12])
			continue
		}

		ports, err := docker.InspectPorts(ctx, m.cfg.Host, controlPath, containerID)
		if err != nil {
			m.logger.Warn("failed to inspect container during startup",
				"containerID", containerID[:12],
				"error", err.Error())
			continue
		}

		if len(ports) > 0 {
			m.logger.Info("startup: adding container to desired state",
				"containerID", containerID[:12],
				"ports", ports)
			m.state.SetDesired(containerID, ports)
		}
	}

	// Reconcile to establish forwards
	// Note: Errors during startup reconciliation are expected (port conflicts, etc.)
	// and should not be fatal. We log them but continue operation.
	if err := m.triggerReconcile(ctx); err != nil {
		m.logger.Warn("startup reconciliation encountered errors (this is normal for port conflicts)",
			"error", err.Error())
		// Don't return error - port conflicts are expected and non-fatal
	}

	m.logger.Info("startup reconciliation complete",
		"containers", len(containerIDs))

	return nil
}

// hasTestInfrastructureLabel checks if a container has the rdhpf.test-infrastructure label
func (m *Manager) hasTestInfrastructureLabel(ctx context.Context, controlPath, containerID string) bool {
	sshHost, port, err := ssh.ParseHost(m.cfg.Host)
	if err != nil {
		m.logger.Warn("failed to parse SSH host", "error", err.Error())
		return false
	}

	// Inspect container labels
	dockerCmd := fmt.Sprintf("docker inspect --format '{{index .Config.Labels \"%s\"}}' %s", docker.LabelTestInfrastructure, containerID)
	remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)

	args := []string{"-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshHost, remoteCmd)

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(output)) == "true"
}

// validateDockerConnectivity performs a quick test of Docker daemon connectivity
func (m *Manager) validateDockerConnectivity(ctx context.Context, controlPath string) error {
	sshHost, port, err := ssh.ParseHost(m.cfg.Host)
	if err != nil {
		return fmt.Errorf("failed to parse SSH host: %w", err)
	}

	// Simple docker version command to test connectivity
	dockerCmd := "docker version --format '{{.Server.Version}}'"
	remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)
	args := []string{"-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, sshHost, remoteCmd)

	// Create a short timeout context for this test
	testCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// #nosec G204 - SSH command with validated host format
	cmd := exec.CommandContext(testCtx, "ssh", args...)

	testStart := time.Now()
	output, err := cmd.Output()
	testDuration := time.Since(testStart)

	if err != nil {
		m.logger.Warn("DIAGNOSTIC: Docker connectivity test failed",
			"duration_ms", testDuration.Milliseconds(),
			"error", err.Error())
		return fmt.Errorf("docker connectivity test failed: %w", err)
	}

	m.logger.Info("DIAGNOSTIC: Docker connectivity test succeeded",
		"duration_ms", testDuration.Milliseconds(),
		"docker_version", strings.TrimSpace(string(output)))

	return nil
}

// startStateWriter runs background state file updates
func (m *Manager) startStateWriter(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Write final state before exit
			forwards := m.state.GetActual()
			history := m.history.GetAll()
			if err := m.stateWriter.Write(forwards, history); err != nil {
				m.logger.Warn("failed to write final state", "error", err)
			}
			return

		case <-ticker.C:
			forwards := m.state.GetActual()
			history := m.history.GetAll()
			if err := m.stateWriter.Write(forwards, history); err != nil {
				m.logger.Warn("failed to write state", "error", err)
			}
		}
	}
}

// cleanupAllForwards removes all active port forwards on shutdown.
// Uses a background context to ensure cleanup completes even if original context is canceled.
func (m *Manager) cleanupAllForwards(ctx context.Context) {
	m.logger.Info("cleaning up all port forwards on shutdown")

	// Add all current forwards to history before removing them
	for _, forward := range m.state.GetActual() {
		m.history.Add(state.HistoryEntry{
			ContainerID: forward.ContainerID,
			Port:        forward.Port,
			StartedAt:   forward.CreatedAt,
			EndedAt:     time.Now(),
			EndReason:   "rdhpf shutdown",
			FinalStatus: forward.Status,
		})
	}

	// Get all containers with active forwards
	containers := m.state.GetAllContainers()

	// Clear desired state for all containers (signal all forwards should be removed)
	for _, containerID := range containers {
		m.state.SetDesired(containerID, []int{})
	}

	// Run final reconciliation to remove all forwards
	if err := m.triggerReconcile(ctx); err != nil {
		m.logger.Warn("cleanup reconciliation encountered errors",
			"error", err.Error())
	} else {
		m.logger.Info("all port forwards removed successfully")
	}
}
