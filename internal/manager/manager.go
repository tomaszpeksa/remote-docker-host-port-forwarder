package manager

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/util"
)

// dockerPingRunner generates health-ping events on the monitored daemon.
type dockerPingRunner interface {
	Ping(ctx context.Context) error
}

// remoteDockerPingRunner uses the application's SSH target and owned mux path,
// not the local Docker client's ambient host/context/TLS configuration.
type remoteDockerPingRunner struct {
	host, controlPath string
	logger            *slog.Logger
}

func newRemoteDockerPingRunner(host, controlPath string, logger *slog.Logger) dockerPingRunner {
	return &remoteDockerPingRunner{host: host, controlPath: controlPath, logger: logger}
}

func (r *remoteDockerPingRunner) Ping(ctx context.Context) error {
	host, port, err := ssh.ParseHost(r.host)
	if err != nil {
		return fmt.Errorf("failed to parse SSH host: %w", err)
	}
	name := fmt.Sprintf("rdhpf-ping-%d", time.Now().UnixNano())
	remoteCmd := "docker run --rm --name " + name + " --label rdhpf.health-check=true alpine:latest echo works"
	args := []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", r.controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", host, remoteCmd)

	// #nosec G204 - Fixed SSH executable, separated destination and generated numeric ping name.
	cmd := exec.CommandContext(ctx, "ssh", args...)
	stdout, stderr := util.NewTailBuffer(64<<10), util.NewTailBuffer(64<<10)
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = stdout, stderr, 2*time.Second
	err = cmd.Run()
	if err != nil {
		r.logger.Warn("event health ping docker run failed",
			"error", err.Error(),
			"output", stdout.String(), "stderr", stderr.String(),
			"truncated", stdout.Truncated() || stderr.Truncated())
		return fmt.Errorf("event health ping docker run failed: %w", err)
	}

	r.logger.Debug("event health ping docker run succeeded",
		"output", strings.TrimSpace(stdout.String()), "stderr", stderr.String(),
		"truncated", stdout.Truncated() || stderr.Truncated())
	return nil
}

// eventWatchdog monitors Docker event stream health via periodic pings
type eventWatchdog struct {
	now           func() time.Time
	dockerPing    dockerPingRunner
	ticks         func() (<-chan time.Time, func())
	idleThreshold time.Duration // 30s - when to start pinging
	fatalAfter    time.Duration // 60s - when to consider stream dead

	mu              sync.RWMutex
	lastEvent       time.Time
	armed           bool
	terminal        bool
	watchdogFailure bool
	result          error
	selectedAt      time.Time
	parent          context.Context
	cancel          context.CancelFunc
	stopping        *atomic.Bool
}

func newEventWatchdog(now func() time.Time, ping dockerPingRunner) *eventWatchdog {
	return &eventWatchdog{
		now:        now,
		dockerPing: ping,
		ticks: func() (<-chan time.Time, func()) {
			ticker := time.NewTicker(10 * time.Second)
			return ticker.C, ticker.Stop
		},
		idleThreshold: 30 * time.Second,
		fatalAfter:    60 * time.Second,
	}
}

// arm starts the processing clock once, when Run starts monitoring, not at construction.
func (w *eventWatchdog) arm(parent context.Context, cancel context.CancelFunc, stopping *atomic.Bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.armed {
		return
	}
	w.parent, w.cancel, w.stopping = parent, cancel, stopping
	w.lastEvent, w.armed = w.now(), true
}

// OnEvent records successful handler completion, never admission or probe success.
func (w *eventWatchdog) OnEvent() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.armed && !w.terminal && w.parent.Err() == nil {
		w.lastEvent = w.now()
	}
}

// Tick makes only a timing/terminal decision. Probe work and joins cannot delay it.
func (w *eventWatchdog) Tick() (time.Time, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.armed {
		return time.Time{}, nil
	}
	if w.terminal {
		return time.Time{}, w.result
	}
	if w.parent.Err() != nil {
		w.selectTerminalLocked(nil, false)
		return time.Time{}, w.result
	}
	dt := w.now().Sub(w.lastEvent)
	switch {
	case dt < w.idleThreshold:
		return time.Time{}, nil
	case dt < w.fatalAfter:
		return w.lastEvent.Add(w.fatalAfter), nil
	default:
		w.selectTerminalLocked(fmt.Errorf("event stream unhealthy: no successfully handled Docker events for %s", dt.Round(time.Second)), true)
		return time.Time{}, w.result
	}
}

// selectTerminalLocked is the ordering point for Run's first terminal result.
// A selected nil is terminal too. Commit and cancel before logging or any join.
func (w *eventWatchdog) selectTerminalLocked(candidate error, watchdogFailure bool) {
	if w.terminal {
		return
	}
	if w.parent.Err() != nil {
		candidate, watchdogFailure = nil, false
	}
	w.terminal, w.result, w.watchdogFailure = true, candidate, watchdogFailure
	w.selectedAt = w.now()
	w.stopping.Store(true)
	w.cancel()
}

func (w *eventWatchdog) finish(candidate error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.armed {
		return candidate
	}
	w.selectTerminalLocked(candidate, false)
	return w.result
}

func (w *eventWatchdog) terminalResult() (error, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.result, w.watchdogFailure
}

// Manager orchestrates Docker event handling and port forward reconciliation.
type Manager struct {
	discovery   discoveryState
	cfg         *config.Config
	eventReader *docker.EventReader
	reconciler  *reconcile.Reconciler
	sshMaster   *ssh.Master
	state       *state.State
	logger      *slog.Logger

	// Event stream health monitoring
	watchdog *eventWatchdog

	// Performance metrics
	metrics performanceMetrics

	// State persistence and IPC
	history      *state.History
	stateWriter  snapshotWriter
	socketServer *socket.Server
	startedAt    time.Time

	retiredGeneration uint64 // guarded by the SSH lifecycle gate
	stopping          atomic.Bool
	stopRecovery      func() // Run-owned cancellation/join, never called under the gate
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

type performanceStats struct {
	avgEventTime         time.Duration
	avgReconcileTime     time.Duration
	avgSSHTime           time.Duration
	uptime               time.Duration
	totalEventsProcessed int64
	totalReconciliations int64
}

func (m *performanceMetrics) getStats() (stats performanceStats) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.eventProcessingTimes) > 0 {
		var total time.Duration
		for _, d := range m.eventProcessingTimes {
			total += d
		}
		stats.avgEventTime = total / time.Duration(len(m.eventProcessingTimes))
	}

	if len(m.reconciliationTimes) > 0 {
		var total time.Duration
		for _, d := range m.reconciliationTimes {
			total += d
		}
		stats.avgReconcileTime = total / time.Duration(len(m.reconciliationTimes))
	}

	if len(m.sshCommandTimes) > 0 {
		var total time.Duration
		for _, d := range m.sshCommandTimes {
			total += d
		}
		stats.avgSSHTime = total / time.Duration(len(m.sshCommandTimes))
	}

	stats.uptime = time.Since(m.startTime)
	stats.totalEventsProcessed = m.totalEventsProcessed
	stats.totalReconciliations = m.totalReconciliations
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
//	if err := manager.Run(ctx, owner); err != nil {
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
	dockerPing := newRemoteDockerPingRunner(cfg.Host, sshMaster.ControlPath(), logger)
	startedAt := time.Now()

	m := &Manager{
		cfg:         cfg,
		eventReader: eventReader,
		reconciler:  reconciler,
		sshMaster:   sshMaster,
		state:       state,
		logger:      logger,
		watchdog:    newEventWatchdog(now, dockerPing),
		metrics: performanceMetrics{
			startTime: startedAt,
		},
		history:   history,
		startedAt: startedAt,
	}
	m.initDiscovery()
	return m
}

// Run starts the manager's main event loop.
//
// It performs these operations:
//
//  1. Starts independent startup/audit inventory and reconciliation workers
//  2. Subscribes to Docker events with joined, generation-fenced delivery
//  3. Handles start/die/stop events as they arrive
//  4. Automatically restarts event stream on failures with exponential backoff
//  5. Continues until context is canceled
//
// Example usage:
//
//	ctx, cancel := context.WithCancel(context.Background())
//	defer cancel()
//
//	if err := manager.Run(ctx, owner); err != nil {
//	    log.Fatal(err)
//	}
func (m *Manager) Run(ctx context.Context, owner *instancepath.Owner) (runErr error) {
	parent := ctx
	socketPath, statePath, err := owner.Paths(m.cfg.Host)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.logger.Info("manager starting")

	// Admission covers initialization, joined publication and cleanup. Pass
	// frozen paths, not lease capabilities, to the concrete publishers.
	m.stateWriter = statefile.NewWriterAtPath(m.cfg.Host, statePath, m.startedAt)
	m.socketServer, err = socket.NewServerAtPath(m.cfg.Host, socketPath, m.state, m.history, m.startedAt, m.logger)
	if err != nil {
		m.logger.Warn("failed to create socket server, status will use file only", "error", err)
	} else {
		m.logger.Info("socket server started")
	}
	defer m.startPublishers(ctx)()
	if err := ctx.Err(); err != nil {
		return err
	}

	// This finalizer is installed before any monitored work. Publisher retirement
	// remains outside it, so admission stays owned through all local completion.
	m.watchdog.arm(parent, cancel, &m.stopping)
	watchdogDone := make(chan struct{})
	var activeStream <-chan docker.StreamResult
	cleaned := false
	cleanup := func() {
		_ = m.watchdog.finish(nil)
		if !cleaned {
			cleaned = true
			m.cleanupAllForwards(context.Background())
		}
	}
	defer func() {
		runErr = m.watchdog.finish(runErr)
		if activeStream != nil {
			<-activeStream
			for len(m.discovery.events) > 0 {
				m.retireEvent((<-m.discovery.events).token)
			}
		}
		m.stopRecovery()
		if _, watchdogFailure := m.watchdog.terminalResult(); watchdogFailure {
			cleanup()
		}
		<-watchdogDone
	}()
	ticks, stopTicker := m.watchdog.ticks()
	go func() { defer close(watchdogDone); defer stopTicker(); m.startEventWatchdogLoop(ctx, ticks) }()

	// Changes are nonblocking wakeups, not state-bearing callbacks. Every
	// reconciliation, including this worker, uses the Master's lifecycle gate.
	recoveryDone := make(chan struct{})
	go func() { defer close(recoveryDone); m.runReconciliation(ctx) }()
	discoveryDone := make(chan struct{})
	go func() { defer close(discoveryDone); m.runDiscovery(ctx) }()
	m.stopRecovery = func() {
		m.stopping.Store(true)
		cancel()
		m.sshMaster.StopHealthMonitor()
		<-recoveryDone
		<-discoveryDone
	}

	// Start SSH health monitor (check every 15 seconds for faster failure detection)
	m.sshMaster.StartHealthMonitor(ctx, 15*time.Second)
	m.logger.Info("SSH health monitoring started",
		"check_interval", "15s",
		"control_path", m.sshMaster.ControlPath())

	// Start performance metrics logger (log every 5 minutes)
	go m.logPerformanceMetrics(ctx, 5*time.Minute)

	// Start event stream watchdog (checks every 10s, pings after 30s idle, fatal after 60s)
	m.logger.Info("event stream watchdog started",
		"tick_interval", "10s",
		"idle_threshold", "30s",
		"fatal_after", "60s")

	// Event stream restart logic with exponential backoff
	// Spec: 1s, 2s, 4s, 8s, max 30s; max 10 consecutive failures
	maxConsecutiveFailures := 10
	consecutiveFailures := 0
	baseDelay := 1 * time.Second

	for {
		// Check if context is canceled before starting/restarting
		if ctx.Err() != nil {
			m.logger.Info("manager stopping due to context cancellation")
			cleanup()
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
		controlPath := m.sshMaster.ControlPath()
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

		if ctx.Err() != nil {
			cleanup()
			return nil
		}

		// Start event stream with healthy ControlMaster
		streamStartTime := time.Now()
		m.logger.Info("DIAGNOSTIC: Starting Docker events stream",
			"attempt_number", consecutiveFailures+1,
			"max_attempts", maxConsecutiveFailures,
			"timestamp", streamStartTime.Format(time.RFC3339))

		m.discovery.mu.Lock()
		generation := m.discovery.generation
		m.discovery.mu.Unlock()
		terminal := m.discovery.openStream(ctx, func(ctx context.Context, event docker.Event) error { return m.deliverEvent(ctx, generation, event) })
		activeStream = terminal

		if consecutiveFailures > 0 {
			m.logger.Warn("event stream restarted",
				"attempt", consecutiveFailures+1,
				"max_failures", maxConsecutiveFailures)
		} else {
			m.logger.Info("manager event loop started")
		}

		// Run event loop until stream closes or errors
		streamClosed := m.runEventLoop(ctx, terminal, generation)
		<-activeStream
		activeStream = nil
		if err := m.finishStream(generation); err != nil {
			return err
		}
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
			cleanup()
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
				m.markRepair()
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
			// Even a clean EOF must not spin through connection checks and
			// replacement. Run owns this timer after the old stream has joined.
			timer := time.NewTimer(baseDelay)
			m.logger.Debug("clean event stream ended, waiting before replacement", "delay", baseDelay)
			select {
			case <-timer.C:
				timer.Stop()
			case <-ctx.Done():
				timer.Stop()
				cleanup()
				return nil
			}
		}
	}
}

// startEventWatchdogLoop owns exactly one probe, including its completion.
func (m *Manager) startEventWatchdogLoop(ctx context.Context, ticks <-chan time.Time) {
	var probeDone chan struct{}
	var cancelProbe context.CancelFunc
	defer func() {
		if probeDone != nil {
			cancelProbe()
			<-probeDone
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-probeDone:
			cancelProbe()
			probeDone, cancelProbe = nil, nil
		case <-ticks:
			// The payload may be stale after scheduling delay. Tick samples now.
			deadline, err := m.watchdog.Tick()
			if err != nil {
				if _, watchdogFailure := m.watchdog.terminalResult(); !watchdogFailure {
					return
				}
				m.watchdog.mu.RLock()
				selectedAt, lastEvent := m.watchdog.selectedAt, m.watchdog.lastEvent
				m.watchdog.mu.RUnlock()
				m.logger.Error("event stream watchdog detected failure, shutting down",
					"error", err, "selected_at", selectedAt, "last_successful_event", lastEvent)
				return
			}
			if !deadline.IsZero() && probeDone == nil && ctx.Err() == nil {
				probeCtx, cancel := context.WithDeadline(ctx, deadline)
				cancelProbe, probeDone = cancel, make(chan struct{})
				go func(done chan struct{}) { defer close(done); _ = m.watchdog.dockerPing.Ping(probeCtx) }(probeDone)
			}
		}
	}
}

// runEventLoop processes events until the stream closes or errors.
// Implements debouncing: instead of reconciling on every event, it batches
// events together and reconciles 200ms after the last event received.
//
// Returns true if stream closed cleanly, false otherwise
func (m *Manager) runEventLoop(ctx context.Context, terminal <-chan docker.StreamResult, generation uint64) bool {
	events := m.discovery.events
	// Debouncing timer - reconcile 200ms after last event
	var debounceTimer *time.Timer
	var eventCount int
	var result *docker.StreamResult

	// Helper to reset/create timer
	resetDebounceTimer := func() {
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.NewTimer(200 * time.Millisecond)
	}

	for {
		if ctx.Err() != nil {
			_ = m.watchdog.finish(nil)
			if terminal != nil {
				<-terminal
			}
			for len(events) > 0 {
				m.retireEvent((<-events).token)
			}
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return true
		}
		if result != nil && len(events) == 0 {
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			m.wakeReconciliation()
			return result.Err == nil || result.Canceled
		}
		select {
		case <-ctx.Done():
			continue

		case completed := <-terminal:
			result = &completed
			terminal = nil
			if completed.Err != nil && !completed.Canceled {
				m.logger.Error("event stream error", "error", completed.Err)
			}

		case accepted, ok := <-events:
			event := accepted.event
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
				if err := m.handleStartEvent(ctx, event, generation); err != nil {
					m.logger.Error("failed to handle start event",
						"containerID", event.ContainerID[:12],
						"error", err.Error())
				} else {
					eventCount++
					resetDebounceTimer()
					// Only successful handler completion is processing health.
					m.watchdog.OnEvent()
				}

			case "die", "stop":
				if err := m.handleStopEvent(ctx, event, generation); err != nil {
					m.logger.Error("failed to handle stop event",
						"containerID", event.ContainerID[:12],
						"error", err.Error())
				} else {
					eventCount++
					resetDebounceTimer()
					// Only successful handler completion is processing health.
					m.watchdog.OnEvent()
				}

			default:
				m.logger.Warn("unexpected event type",
					"type", event.Type,
					"containerID", event.ContainerID[:12])
			}

			m.retireEvent(accepted.token)

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

				m.wakeReconciliation()

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
func (m *Manager) handleStartEvent(ctx context.Context, event docker.Event, generation uint64) error {
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
	controlPath := m.sshMaster.ControlPath()

	// Inspect container to get published ports
	cmdStart := time.Now()
	ports, err := docker.InspectPorts(ctx, m.cfg.Host, controlPath, event.ContainerID)
	m.metrics.recordSSHCommand(time.Since(cmdStart))

	if err != nil {
		m.markRepair()
		return fmt.Errorf("failed to inspect container ports: %w", err)
	}

	m.logger.Info("container ports discovered",
		"containerID", event.ContainerID[:12],
		"ports", ports)

	// Update desired state
	if err := m.setEventDesired(ctx, generation, event.ContainerID, ports); err != nil {
		return err
	}

	// Note: We don't reconcile immediately anymore
	// The runEventLoop handles debounced reconciliation

	return nil
}

// handleStopEvent processes a container stop or die event.
//
// Steps:
//  1. Clear desired state for the container (set to empty ports)
//  2. Trigger debounced reconciliation
func (m *Manager) handleStopEvent(ctx context.Context, event docker.Event, generation uint64) error {
	m.logger.Info("handling container stop",
		"containerID", event.ContainerID[:12])

	// Clear desired state (empty ports = no forwards wanted)
	if err := m.setEventDesired(ctx, generation, event.ContainerID, []int{}); err != nil {
		return err
	}

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
	if m.stopping.Load() {
		return context.Canceled
	}
	return m.sshMaster.WithForwarding(ctx, func(connection *ssh.Forwarding) error {
		return m.reconcileConnection(ctx, connection)
	})
}

// reconcileConnection runs within WithConnection's gate. Keeping this operation
// separate makes unavailable-connection and stale-wakeup handling explicit.
func (m *Manager) reconcileConnection(ctx context.Context, connection *ssh.Forwarding) error {
	if m.stopping.Load() {
		return context.Canceled
	}
	retired := connection.Generation() != m.retiredGeneration
	m.invalidateRetired(connection.Generation())
	if !connection.Ready() {
		return fmt.Errorf("SSH connection unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.discovery.mu.Lock()
	captured := m.discovery.request
	full := retired || captured > m.discovery.attempted
	m.discovery.mu.Unlock()
	defer func() {
		m.discovery.mu.Lock()
		m.discovery.attempted = max(m.discovery.attempted, captured)
		m.discovery.mu.Unlock()
	}()
	if full {
		return m.reconcileLocked(ctx, connection)
	}
	return m.applyActions(ctx, connection, m.reconciler.RetryActions())
}

// invalidateRetired is called only inside the SSH lifecycle gate, before Diff.
// Coalesced or stale wakeups cannot invalidate a newer connection's forwards.
func (m *Manager) invalidateRetired(generation uint64) {
	if generation == m.retiredGeneration {
		return
	}
	at := time.Now()
	for _, forward := range m.state.InvalidateConnection(at) {
		m.history.Add(state.HistoryEntry{
			ContainerID: forward.ContainerID, Port: forward.Port,
			StartedAt: forward.CreatedAt, EndedAt: at,
			EndReason: "container stopped", FinalStatus: forward.Status,
		})
	}
	m.retiredGeneration = generation
}

func (m *Manager) reconcileLocked(ctx context.Context, connection *ssh.Forwarding) error {
	toAdd, toRemove := m.reconciler.Diff()
	return m.applyActions(ctx, connection, append(toRemove, toAdd...))
}

func (m *Manager) applyActions(ctx context.Context, connection *ssh.Forwarding, actions []reconcile.Action) error {
	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		m.metrics.recordReconciliation(duration)
		m.logger.Debug("reconciliation time",
			"duration_ms", duration.Milliseconds())
	}()

	if len(actions) == 0 {
		m.logger.Debug("reconciliation: no actions needed")
		return nil
	}

	m.logger.Info("reconciliation starting",
		"actions", len(actions))

	if err := m.reconciler.Apply(ctx, connection, actions); err != nil {
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
			stats := m.metrics.getStats()

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
				"avg_event_latency_ms", stats.avgEventTime.Milliseconds(),
				"avg_reconcile_ms", stats.avgReconcileTime.Milliseconds(),
				"avg_ssh_cmd_ms", stats.avgSSHTime.Milliseconds(),
				"active_forwards", activeCount,
				"conflicts", conflictCount,
				"total_events", stats.totalEventsProcessed,
				"total_reconciliations", stats.totalReconciliations,
				"uptime", stats.uptime.Round(time.Second).String())
		}
	}
}

// listRunning captures a complete running-container list without changing State.
//
// This ensures that if the tool is started while containers are already running,
// it will establish forwards for them.
//
// Steps:
//  1. Execute `docker ps --no-trunc --format '{{.ID}}'` via SSH to get full running container IDs
//  2. For each container, inspect its ports
//  3. Update desired state
//  4. Perform reconciliation
func (m *Manager) listRunning(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	m.logger.Info("performing startup reconciliation")

	// Get control path for SSH commands
	controlPath := m.sshMaster.ControlPath()

	// Parse host and port from SSH URL
	sshHost, port, err := ssh.ParseHost(m.cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSH host: %w", err)
	}

	// Get list of running containers
	// Build the docker command as a single quoted string to protect {{.ID}} from shell expansion
	dockerCmd := "docker ps --no-trunc --format '{{.ID}}'"

	// Build SSH command args
	// Important: sh -c and the docker command must be passed as a single argument to SSH
	remoteCmd := fmt.Sprintf("sh -c %q", dockerCmd)
	args := []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", sshHost, remoteCmd)

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)

	stdout, stderr := util.NewLimitedBuffer(4<<20), util.NewLimitedBuffer(64<<10)
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = stdout, stderr, 2*time.Second
	err = cmd.Run()
	if stdout.Err() != nil {
		return nil, stdout.Err()
	}
	if stderr.Err() != nil {
		return nil, stderr.Err()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	output := stdout.Bytes()
	if err != nil {
		return nil, fmt.Errorf("failed to list running containers: %w", err)
	}

	// Parse container IDs
	containerIDs := make([]string, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) < 12 || len(strings.Fields(line)) != 1 {
				return nil, fmt.Errorf("malformed container list record")
			}
			containerIDs = append(containerIDs, line)
			if len(containerIDs) > 10000 {
				return nil, fmt.Errorf("container list limit exceeded")
			}
		}
	}

	m.logger.Info("found running containers",
		"count", len(containerIDs))

	sort.Strings(containerIDs)
	return containerIDs, nil
}

// hasTestInfrastructureLabel checks if a container has the rdhpf.test-infrastructure label
func (m *Manager) hasTestInfrastructureLabel(ctx context.Context, controlPath, containerID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sshHost, port, err := ssh.ParseHost(m.cfg.Host)
	if err != nil {
		m.logger.Warn("failed to parse SSH host", "error", err.Error())
		return false, err
	}

	operand, err := ssh.QuoteShellArg(containerID)
	if err != nil {
		m.logger.Warn("invalid container operand", "error", err.Error())
		return false, err
	}
	dockerCmd := fmt.Sprintf("docker inspect --format '{{index .Config.Labels \"%s\"}}' -- %s", docker.LabelTestInfrastructure, operand)
	quoted, err := ssh.QuoteShellArg(dockerCmd)
	if err != nil {
		return false, err
	}
	remoteCmd := "sh -c " + quoted

	args := []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", sshHost, remoteCmd)

	// #nosec G204 - SSH command with validated host format (checked in config.Validate)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	stdout, stderr := util.NewLimitedBuffer(1<<20), util.NewLimitedBuffer(64<<10)
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = stdout, stderr, 2*time.Second
	err = cmd.Run()
	if stdout.Err() != nil {
		return false, stdout.Err()
	}
	if stderr.Err() != nil {
		return false, stderr.Err()
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	output := stdout.Bytes()
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(string(output)) == "true", nil
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
	args := []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", controlPath}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", sshHost, remoteCmd)

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
	if m.stopRecovery != nil {
		m.stopRecovery()
	}
	err := m.sshMaster.WithForwarding(ctx, func(connection *ssh.Forwarding) error {
		m.invalidateRetired(connection.Generation())
		return m.cleanupForwardsLocked(ctx, connection)
	})
	if err != nil {
		m.logger.Warn("cleanup reconciliation encountered errors", "error", err)
	}
}

func (m *Manager) cleanupForwardsLocked(ctx context.Context, connection *ssh.Forwarding) error {
	m.logger.Info("cleaning up all port forwards on shutdown")

	// Get all containers with active forwards
	containers := m.state.GetAllContainers()

	// Clear desired state for all containers (signal all forwards should be removed)
	for _, containerID := range containers {
		m.state.SetDesired(containerID, []int{})
	}

	// The existing cleanup policy remains; this path cannot start a connection.
	if !connection.Ready() {
		return fmt.Errorf("SSH connection unavailable during cleanup")
	}
	_, remove := m.reconciler.Diff()
	for i := range remove {
		remove[i].EndReason = "rdhpf shutdown"
	}
	return m.applyActions(ctx, connection, remove)
}
