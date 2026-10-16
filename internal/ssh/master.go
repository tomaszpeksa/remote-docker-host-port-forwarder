package ssh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type circuitState int

const (
	circuitClosed circuitState = iota
	circuitOpen
	circuitHalfOpen
)

const (
	masterStartupTimeout = 10 * time.Second
	masterCheckTimeout   = 2 * time.Second
	masterRetireTimeout  = time.Second
)

// ErrRetirementUncertain means the owner must retain cleanup and admission
// responsibility: completion of the retained child has not been established.
var ErrRetirementUncertain = errors.New("owned SSH retirement uncertain; replacement prohibited")

type masterLifetime struct {
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	finished  sync.Once
	closed    chan struct{}
}

func newMasterLifetime() *masterLifetime {
	ctx, cancel := context.WithCancel(context.Background())
	return &masterLifetime{ctx: ctx, cancel: cancel, closed: make(chan struct{})}
}

type masterConnection struct {
	cmd     *exec.Cmd
	done    chan struct{}
	waitErr error // published by closing done; exactly one goroutine calls Wait
	socket  os.FileInfo
	retired bool
}

type healthMonitor struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Master owns one foreground SSH child. The lifecycle gate also protects entire
// Manager forwarding transactions, not just individual connection commands.
// Forced parent termination can leave the child alive; normal Close owns reaping.
type Master struct {
	host                string
	controlPath         string
	namespace           *masterNamespace
	logger              *slog.Logger
	mu                  sync.Mutex
	cmd                 *exec.Cmd // last started command, retained after its Wait for diagnostics
	connection          *masterConnection
	ready               bool
	generation          uint64 // advances only after proven retirement
	lifetime            atomic.Pointer[masterLifetime]
	changes             chan struct{}
	circuitState        circuitState
	consecutiveFailures int
	lastFailureTime     time.Time
	healthMonitorMu     sync.Mutex
	monitor             *healthMonitor
}

// NewMaster allocates a private namespace without starting SSH. Callers must
// register Close immediately, including when Open is never called or fails.
func NewMaster(host string, logger *slog.Logger) (*Master, error) {
	if !strings.HasPrefix(host, "ssh://") {
		return nil, fmt.Errorf("host must be in ssh://user@host format, got: %s", host)
	}
	if strings.TrimPrefix(host, "ssh://") == "" {
		return nil, fmt.Errorf("host cannot be empty after ssh:// prefix")
	}
	namespace, err := newMasterNamespace()
	if err != nil {
		return nil, fmt.Errorf("failed to allocate SSH namespace: %w", err)
	}
	m := &Master{host: host, controlPath: namespace.path, namespace: namespace, logger: logger, changes: make(chan struct{}, 1)}
	m.lifetime.Store(newMasterLifetime())
	return m, nil
}

// ControlPath returns the configured mux endpoint, not proof of its ownership.
func (m *Master) ControlPath() string { return m.controlPath }

// Changes coalesces notifications. Consumers must observe generation under
// WithConnection, rather than infer state or ordering from a notification.
func (m *Master) Changes() <-chan struct{} { return m.changes }
func (m *Master) notify() {
	select {
	case m.changes <- struct{}{}:
	default:
	}
}

// WithConnection serializes state invalidation and complete Diff/Apply cycles
// with replacement/Close. The operation must not re-enter Master. Unavailable
// connections still expose proven retirement, but must not execute forwards.
func (m *Master) WithConnection(ctx context.Context, operation func(uint64, bool) error) error {
	life := m.lifetime.Load()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.operationAllowed(ctx, life); err != nil {
		return err
	}
	ready := m.ready && m.checkConnection(ctx) == nil
	if err := m.operationAllowed(ctx, life); err != nil {
		return err
	}
	return operation(m.generation, ready)
}

func (m *Master) operationAllowed(ctx context.Context, life *masterLifetime) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if life != m.lifetime.Load() || life.ctx.Err() != nil {
		return fmt.Errorf("SSH lifecycle stopped: %w", context.Canceled)
	}
	return nil
}

// Open explicitly starts a lifecycle, including reuse after a completed Close.
// After readiness, the child no longer depends on this operation's context.
func (m *Master) Open(ctx context.Context) error {
	m.mu.Lock()
	life := m.lifetime.Load()
	if life.ctx.Err() != nil {
		select {
		case <-life.closed:
			life = newMasterLifetime()
			m.lifetime.Store(life)
		default:
			m.mu.Unlock()
			return fmt.Errorf("SSH close still in progress")
		}
	}
	err := m.operationAllowed(ctx, life)
	if err == nil && m.connection != nil {
		err = fmt.Errorf("SSH connection already owned")
	}
	if err == nil {
		err = m.openConnection(ctx, life)
		if err == nil {
			m.consecutiveFailures = 0
			m.circuitState = circuitClosed
		}
	}
	m.mu.Unlock()
	m.notify()
	return err
}

func (m *Master) openConnection(ctx context.Context, life *masterLifetime) error {
	if err := m.namespace.open(); err != nil {
		return err
	}
	if _, err := os.Lstat(m.controlPath); err == nil {
		return fmt.Errorf("refusing occupied SSH control path %s", m.controlPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	sshHost, port, err := ParseHost(m.host)
	if err != nil {
		return err
	}
	args := []string{"-MN", "-o", "ControlMaster=yes", "-o", "ForkAfterAuthentication=no", "-o", "ControlPersist=no",
		"-o", "ControlPath=" + m.controlPath, "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"-o", "TCPKeepAlive=yes", "-o", "ExitOnForwardFailure=yes", "-o", "BatchMode=yes"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", sshHost)
	// The retained child, not a context watcher or a daemon launcher, owns SSH.
	// #nosec G204 -- Fixed ssh executable, separate port/control-path operands, destination after --; no local shell.
	// Host and inherited SSH configuration are operator-configured; this is not general SSH input/configuration safety.
	cmd := exec.Command("ssh", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start SSH ControlMaster: %w", err)
	}
	c := &masterConnection{cmd: cmd, done: make(chan struct{})}
	m.cmd = cmd
	m.connection = c
	m.ready = false
	go func() { c.waitErr = cmd.Wait(); close(c.done) }()
	startup, cancel := context.WithTimeout(ctx, masterStartupTimeout)
	stop := context.AfterFunc(life.ctx, cancel)
	defer cancel()
	defer stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := startup.Err(); err != nil {
			return errors.Join(err, m.retireConnection())
		}
		select {
		case <-c.done:
			return errors.Join(fmt.Errorf("SSH exited during startup: %v", c.waitErr), m.retireConnection())
		default:
		}
		if _, err := os.Lstat(m.controlPath); err == nil {
			if err := m.checkConnection(startup); err != nil {
				return errors.Join(fmt.Errorf("SSH startup association: %w", err), m.retireConnection())
			}
			if err := startup.Err(); err != nil {
				return errors.Join(err, m.retireConnection())
			}
			m.ready = true
			m.logger.Info("SSH ControlMaster established", "controlPath", m.controlPath, "pid", cmd.Process.Pid)
			return nil
		} else if !os.IsNotExist(err) {
			return errors.Join(err, m.retireConnection())
		}
		select {
		case <-startup.Done():
		case <-c.done:
		case <-ticker.C:
		}
	}
}

var masterPIDPattern = regexp.MustCompile(`(?m)^Master running \(pid=([0-9]+)\)\r?$`)

// associate verifies the kernel peer identity against a still-owned, unreaped
// child, and checks that the pathname did not change during connection. Never
// trust a mux-reported PID as authority to signal or unlink anything.
func (m *Master) associate(ctx context.Context) error {
	if err := m.namespace.verify(); err != nil {
		return err
	}
	c := m.connection
	if c == nil {
		return fmt.Errorf("no owned SSH child")
	}
	select {
	case <-c.done:
		return fmt.Errorf("owned SSH child exited")
	default:
	}
	before, err := os.Lstat(m.controlPath)
	if err != nil {
		return err
	}
	if before.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("SSH endpoint is not a socket")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", m.controlPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	pid, err := unixPeerPID(conn.(*net.UnixConn))
	if err != nil {
		return err
	}
	after, err := os.Lstat(m.controlPath)
	if err != nil {
		return err
	}
	if pid != c.cmd.Process.Pid || !os.SameFile(before, after) || (c.socket != nil && !os.SameFile(c.socket, after)) {
		return fmt.Errorf("SSH endpoint does not belong to the owned child")
	}
	// Process.Signal uses the retained handle's done state as well as the
	// kernel process. This also rejects the interval between Wait reaping the
	// child and publishing done; a reused numeric PID is not ownership.
	if err := c.cmd.Process.Signal(syscall.Signal(0)); err != nil {
		return fmt.Errorf("owned SSH child no longer live: %w", err)
	}
	select {
	case <-c.done:
		return fmt.Errorf("owned SSH child exited during association")
	default:
	}
	c.socket = after
	return nil
}

func (m *Master) checkConnection(ctx context.Context) error {
	check, cancel := context.WithTimeout(ctx, masterCheckTimeout)
	defer cancel()
	if err := m.associate(check); err != nil {
		return err
	}
	host, port, err := ParseHost(m.host)
	if err != nil {
		return err
	}
	args := []string{"-o", "BatchMode=yes", "-S", m.controlPath, "-O", "check"}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", host)
	// #nosec G204 - Existing SSH host/input policy; arguments remain separate.
	output, err := exec.CommandContext(check, "ssh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("SSH check: %w", err)
	}
	match := masterPIDPattern.FindSubmatch(output)
	if len(match) != 2 {
		return fmt.Errorf("SSH check did not report a master PID")
	}
	pid, err := strconv.Atoi(string(match[1]))
	if err != nil || pid != m.connection.cmd.Process.Pid {
		return fmt.Errorf("SSH check reported a different master")
	}
	return m.associate(check)
}

// Check verifies both ownership and mux responsiveness, with a local 2s bound.
func (m *Master) Check() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ready {
		return fmt.Errorf("SSH connection unavailable")
	}
	return m.checkConnection(context.Background())
}

// retireConnection runs under the lifecycle gate. Only the retained child
// handle is signaled. Completion precedes generation advance/socket cleanup.
func (m *Master) retireConnection() error {
	c := m.connection
	if c == nil {
		return nil
	}
	m.ready = false
	select {
	case <-c.done:
	default:
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(masterRetireTimeout)
		select {
		case <-c.done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = c.cmd.Process.Kill()
			timer.Reset(masterRetireTimeout)
			select {
			case <-c.done:
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
				return ErrRetirementUncertain
			}
		}
	}
	if c.cmd.ProcessState == nil {
		return errors.Join(ErrRetirementUncertain, fmt.Errorf("owned SSH Wait did not establish retirement: %v", c.waitErr))
	}
	if !c.retired {
		m.generation++
		c.retired = true
	}
	if err := m.namespace.reclaim(); err != nil {
		return err
	}
	m.connection = nil
	return nil
}

// EnsureAlive checks/replaces one connection atomically. It never stops the
// monitor. Queued work from a canceled lifecycle cannot affect a later Open.
func (m *Master) EnsureAlive(ctx context.Context) error {
	life := m.lifetime.Load()
	m.mu.Lock()
	previousGeneration, previousConnection := m.generation, m.connection
	err := m.operationAllowed(ctx, life)
	if err == nil {
		err = m.ensureAlive(ctx, life)
	}
	changed := previousGeneration != m.generation || previousConnection != m.connection
	m.mu.Unlock()
	if changed {
		m.notify()
	}
	return err
}
func (m *Master) ensureAlive(ctx context.Context, life *masterLifetime) error {
	if m.circuitState == circuitOpen {
		if time.Since(m.lastFailureTime) <= 60*time.Second {
			return fmt.Errorf("circuit breaker open: too many consecutive failures")
		}
		m.circuitState = circuitHalfOpen
	}
	if m.ready && m.checkConnection(ctx) == nil {
		return nil
	}
	// Incidental cancellation is not evidence that a successful connection
	// failed. Only the check's own deadline may initiate retirement here.
	if err := m.operationAllowed(ctx, life); err != nil {
		return err
	}
	if err := m.retireConnection(); err != nil {
		return err
	}
	if err := m.operationAllowed(ctx, life); err != nil {
		return err
	}
	if err := m.openConnection(ctx, life); err != nil {
		m.consecutiveFailures++
		m.lastFailureTime = time.Now()
		if m.consecutiveFailures >= 5 {
			m.circuitState = circuitOpen
		}
		return fmt.Errorf("failed to recreate SSH ControlMaster: %w", err)
	}
	m.consecutiveFailures = 0
	m.circuitState = circuitClosed
	m.logger.Info("SSH ControlMaster recreated successfully")
	return nil
}

// Close fences automatic work before joining or waiting for the gate. No join
// holds a lock needed by the monitor. Explicit Open is allowed after completion.
func (m *Master) Close() error {
	life := m.lifetime.Load()
	life.closeOnce.Do(func() {
		life.cancel()
		m.StopHealthMonitor()
	})
	m.mu.Lock()
	if life != m.lifetime.Load() {
		m.mu.Unlock()
		return nil // a concurrent explicit Open already began a newer lifecycle
	}
	err := m.retireConnection()
	if err == nil {
		err = m.namespace.close()
	}
	if err == nil {
		life.finished.Do(func() { close(life.closed) })
	}
	m.mu.Unlock()
	m.notify()
	return err
}

// StartHealthMonitor starts at most one monitor for the current lifecycle.
func (m *Master) StartHealthMonitor(ctx context.Context, interval time.Duration) {
	life := m.lifetime.Load()
	m.healthMonitorMu.Lock()
	defer m.healthMonitorMu.Unlock()
	if life.ctx.Err() != nil {
		return
	}
	if m.monitor != nil {
		select {
		case <-m.monitor.done:
		default:
			return
		}
	}
	healthCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life.ctx, cancel)
	monitor := &healthMonitor{cancel: cancel, done: make(chan struct{})}
	m.monitor = monitor
	go func() {
		defer close(monitor.done)
		defer stop()
		defer cancel()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-healthCtx.Done():
				return
			case <-ticker.C:
				if err := m.EnsureAlive(healthCtx); err != nil {
					m.logger.Warn("SSH health check failed", "error", err)
				} else {
					m.logger.Debug("SSH health check passed")
				}
			}
		}
	}()
}

// StopHealthMonitor cancels and joins monitoring without closing the connection.
func (m *Master) StopHealthMonitor() {
	m.healthMonitorMu.Lock()
	monitor := m.monitor
	if monitor != nil {
		monitor.cancel()
	}
	m.healthMonitorMu.Unlock()
	if monitor == nil {
		return
	}
	<-monitor.done
	m.healthMonitorMu.Lock()
	if m.monitor == monitor {
		m.monitor = nil
	}
	m.healthMonitorMu.Unlock()
}
