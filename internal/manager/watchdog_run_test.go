package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
)

type watchdogRun struct {
	m                       *Manager
	clock                   *fakeClock
	ticks                   chan time.Time
	cancel                  context.CancelFunc
	done                    chan struct{}
	err                     error
	stream                  chan context.Context
	stopped                 chan struct{}
	launch                  func()
	startOnce               sync.Once
	beforeRuntimeRetirement func()
}

// Own a real Run, publisher admission, inert SSH processes, and a joined stream.
// Tests may replace the stream before start, not the Manager/handler implementation.
func newWatchdogRun(t *testing.T) *watchdogRun {
	t.Helper()
	m, _, _ := recoveryManager(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("S10_TEST_EXECUTABLE", executable)
	t.Setenv("S10_NATIVE_SSH", "1")
	// This race-instrumented test executable also acts as SSH. Remove only the
	// race runtime's artificial exit sleep in children, not detection or budgets.
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\nexec \"$S10_TEST_EXECUTABLE\" -test.run '^TestWatchdogNativeSSH$' -- \"$@\"\n"), 0700))
	home, err := os.MkdirTemp("/tmp", "s10-run-")
	require.NoError(t, err)
	t.Setenv("HOME", home)
	owner, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	r := &watchdogRun{m: m, clock: newFakeClock(time.Now()), ticks: make(chan time.Time), done: make(chan struct{}), stream: make(chan context.Context, 1), stopped: make(chan struct{})}
	m.watchdog.now = r.clock.Now
	m.watchdog.ticks = func() (<-chan time.Time, func()) { return r.ticks, func() { close(r.stopped) } }
	m.discovery.list = func(context.Context) ([]string, error) { return nil, nil }
	m.discovery.openStream = func(ctx context.Context, _ func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		terminal := make(chan docker.StreamResult, 1)
		r.stream <- ctx
		go func() { <-ctx.Done(); terminal <- docker.StreamResult{Canceled: true}; close(terminal) }()
		return terminal
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	// Register before assertions/launch. All owned work joins before lease/HOME retirement.
	t.Cleanup(func() {
		cancel()
		r.start()
		<-r.done
		if r.m.watchdog.armed {
			<-r.stopped
		}
		// A failed Close retains admission and HOME; Run only stops monitoring.
		require.NoError(t, m.sshMaster.Close(), "MASTER_RETIREMENT_UNCERTAIN: preserving admission and runtime HOME %s", home)
		if r.beforeRuntimeRetirement != nil {
			r.beforeRuntimeRetirement()
		}
		require.NoError(t, owner.Close())
		require.NoError(t, os.RemoveAll(home))
	})
	r.launch = func() { go func() { defer close(r.done); r.err = m.Run(ctx, owner) }() }
	return r
}

func (r *watchdogRun) start() { r.startOnce.Do(r.launch) }

func awaitWatchdog(t *testing.T, ch <-chan struct{}, marker string) bool {
	t.Helper()
	select {
	case <-ch:
		return true
	case <-time.After(time.Second):
		t.Error(marker)
		return false
	}
}
func (r *watchdogRun) tick(t *testing.T) {
	t.Helper()
	select {
	case r.ticks <- time.Unix(0, 0):
	case <-time.After(time.Second):
		t.Fatal("watchdog tick not consumed")
	}
}

func TestManagerSilentOpenStreamWatchdog(t *testing.T) {
	r := newWatchdogRun(t)
	r.start()
	ctx := r.awaitStream(t)
	r.clock.Advance(60 * time.Second)
	r.tick(t)
	if !awaitWatchdog(t, ctx.Done(), "WATCHDOG_MUST_CANCEL_OPEN_STREAM") {
		return
	}
	if !awaitWatchdog(t, r.done, "RUN_MUST_RETAIN_OWNED_JOIN") {
		return
	}
	require.ErrorContains(t, r.err, "no successfully handled Docker events", "WATCHDOG_CAUSE_MUST_SURVIVE")
	require.True(t, r.m.stopping.Load())
	require.Eventually(t, func() bool {
		select {
		case <-r.stopped:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
}

func TestManagerWatchdogActiveInspect(t *testing.T) {
	r := newWatchdogRun(t)
	entered := filepath.Join(os.Getenv("PATH"), "inspect-entered")
	t.Setenv("S10_INSPECT_ENTERED", entered)
	script := "#!/bin/sh\ncase \"$*\" in *HostConfig.PortBindings*) printf entered > \"$S10_INSPECT_ENTERED\"; while :; do :; done;; esac\nexec \"$S10_TEST_EXECUTABLE\" -test.run '^TestWatchdogNativeSSH$' -- \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte(script), 0700))
	streamJoined := make(chan struct{})
	r.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		terminal := make(chan docker.StreamResult, 1)
		r.stream <- ctx
		go func() {
			defer close(streamJoined)
			defer close(terminal)
			_ = deliver(ctx, docker.Event{Type: "start", ContainerID: discoveryA})
			<-ctx.Done()
			terminal <- docker.StreamResult{Canceled: true}
		}()
		return terminal
	}
	r.start()
	ctx := r.awaitStream(t)
	require.Eventually(t, func() bool { _, err := os.Stat(entered); return err == nil }, time.Second, time.Millisecond)
	started := time.Now()
	r.clock.Advance(60 * time.Second)
	r.tick(t)
	if !awaitWatchdog(t, ctx.Done(), "WATCHDOG_MUST_CANCEL_INSPECT_BEFORE_OWN_TIMEOUT") {
		return
	}
	if !awaitWatchdog(t, r.done, "INSPECT_AND_STREAM_MUST_JOIN") {
		return
	}
	require.Less(t, time.Since(started), 5*time.Second, "inspection timeout is not watchdog proof")
	require.ErrorContains(t, r.err, "no successfully handled Docker events", "WATCHDOG_CAUSE_MUST_SURVIVE")
	<-streamJoined
	r.m.discovery.mu.Lock()
	pending := r.m.discovery.pending
	r.m.discovery.mu.Unlock()
	require.Zero(t, pending)
}

func (r *watchdogRun) awaitStream(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-r.stream:
		return ctx
	case <-r.done:
		t.Fatalf("Run returned before stream: %v", r.err)
	case <-time.After(3 * time.Second):
		t.Fatal("owned stream did not enter")
	}
	return nil
}

// Only the private launcher invokes this child. It serves a real owned Unix
// endpoint so Master's kernel PID association and retained-child Wait stay real.
func TestWatchdogNativeSSH(t *testing.T) {
	if os.Getenv("S10_NATIVE_SSH") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return
	}
	args = args[1:]
	path, operation := "", ""
	for i, arg := range args {
		if strings.HasPrefix(arg, "ControlPath=") {
			path = strings.TrimPrefix(arg, "ControlPath=")
		}
		if arg == "-S" && i+1 < len(args) {
			path = args[i+1]
		}
		if arg == "-O" && i+1 < len(args) {
			operation = args[i+1]
		}
	}
	if len(args) > 0 && args[0] == "-MN" {
		listener, err := net.Listen("unix", path)
		if err != nil {
			os.Exit(21)
		}
		for {
			conn, err := listener.Accept()
			if err != nil {
				os.Exit(22)
			}
			_, _ = fmt.Fprintf(conn, "Master running (pid=%d)\n", os.Getpid())
			_ = conn.Close()
		}
	}
	if operation == "check" {
		conn, err := net.Dial("unix", path)
		if err != nil {
			os.Exit(23)
		}
		_, err = io.Copy(os.Stdout, conn)
		_ = conn.Close()
		if err != nil {
			os.Exit(24)
		}
	} else {
		if strings.Contains(strings.Join(args, " "), "HostConfig.PortBindings") && os.Getenv("S10_INSPECT_FAIL") == "1" {
			os.Exit(7)
		}
		_, _ = fmt.Fprintln(os.Stdout, "{}")
	}
	os.Exit(0)
}

type watchdogPingFunc func(context.Context) error

func (f watchdogPingFunc) Ping(ctx context.Context) error { return f(ctx) }

func TestManagerWatchdogProbeJoinDoesNotGateAction(t *testing.T) {
	r := newWatchdogRun(t)
	entered := make(chan context.Context, 4)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	r.m.watchdog.dockerPing = watchdogPingFunc(func(ctx context.Context) error { entered <- ctx; <-release; return ctx.Err() })
	r.start()
	stream := r.awaitStream(t)
	r.clock.Advance(30 * time.Second)
	r.tick(t)
	var probe context.Context
	select {
	case probe = <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not enter")
	}
	r.clock.Advance(30 * time.Second)
	select {
	case r.ticks <- time.Unix(0, 0):
	case <-time.After(time.Second):
		t.Error("PROBE_JOIN_MUST_NOT_GATE_ACTION")
		return
	}
	if !awaitWatchdog(t, stream.Done(), "PROBE_JOIN_MUST_NOT_GATE_ACTION") {
		return
	}
	if !awaitWatchdog(t, probe.Done(), "PROBE_JOIN_MUST_NOT_GATE_ACTION") {
		return
	}
	require.ErrorIs(t, probe.Err(), context.Canceled)
	select {
	case <-r.done:
		t.Error("RUN_MUST_RETAIN_OWNED_JOIN")
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case <-entered:
		t.Error("probe overlapped held attempt")
	default:
	}
	once.Do(func() { close(release) })
	if !awaitWatchdog(t, r.done, "RUN_MUST_RETAIN_OWNED_JOIN") {
		return
	}
	require.ErrorContains(t, r.err, "no successfully handled Docker events")
}

func lastWatchdogHealth(w *eventWatchdog) time.Time {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastEvent
}
func pendingWatchdogEvents(m *Manager) int {
	m.discovery.mu.Lock()
	defer m.discovery.mu.Unlock()
	return m.discovery.pending
}

func TestManagerWatchdogSuccessfulProcessingOnly(t *testing.T) {
	r := newWatchdogRun(t)
	delivery := make(chan func(context.Context, docker.Event) error, 1)
	r.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		done := make(chan docker.StreamResult, 1)
		r.stream <- ctx
		delivery <- deliver
		go func() { <-ctx.Done(); done <- docker.StreamResult{Canceled: true}; close(done) }()
		return done
	}
	r.start()
	ctx := r.awaitStream(t)
	deliver := <-delivery
	t0 := lastWatchdogHealth(r.m.watchdog)
	for _, event := range []string{"failed-start", "failed-start", "start", "die", "stop"} {
		r.clock.Advance(10 * time.Second)
		typ := event
		if event == "failed-start" {
			typ = "start"
			t.Setenv("S10_INSPECT_FAIL", "1")
		} else {
			t.Setenv("S10_INSPECT_FAIL", "")
		}
		require.NoError(t, deliver(ctx, docker.Event{Type: typ, ContainerID: discoveryA}))
		require.Eventually(t, func() bool { return pendingWatchdogEvents(r.m) == 0 }, time.Second, time.Millisecond)
		if event == "failed-start" {
			require.Equal(t, t0, lastWatchdogHealth(r.m.watchdog), "FAILED_PROCESSING_MUST_NOT_RESET")
		} else {
			require.Equal(t, r.clock.Now(), lastWatchdogHealth(r.m.watchdog))
		}
	}
	r.clock.Advance(60 * time.Second)
	r.tick(t)
	if !awaitWatchdog(t, r.done, "WATCHDOG_MUST_CANCEL_OPEN_STREAM") {
		return
	}
	require.Error(t, r.err)
	last := lastWatchdogHealth(r.m.watchdog)
	r.clock.Advance(time.Second)
	r.m.watchdog.OnEvent()
	require.Equal(t, last, lastWatchdogHealth(r.m.watchdog))
}

func TestManagerWatchdogRetiresQueuedAndCanceledEnqueue(t *testing.T) {
	r := newWatchdogRun(t)
	entered := filepath.Join(os.Getenv("PATH"), "held-inspect")
	t.Setenv("S10_INSPECT_ENTERED", entered)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\ncase \"$*\" in *HostConfig.PortBindings*) printf entered > \"$S10_INSPECT_ENTERED\"; while :; do :; done;; esac\nexec \"$S10_TEST_EXECUTABLE\" -test.run '^TestWatchdogNativeSSH$' -- \"$@\"\n"), 0700))
	joined := make(chan struct{})
	deliveryError := make(chan error, 1)
	r.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		terminal := make(chan docker.StreamResult, 1)
		r.stream <- ctx
		go func() {
			defer close(joined)
			defer close(terminal)
			for range 12 {
				if err := deliver(ctx, docker.Event{Type: "start", ContainerID: discoveryA}); err != nil {
					deliveryError <- err
					break
				}
			}
			<-ctx.Done()
			terminal <- docker.StreamResult{Canceled: true}
		}()
		return terminal
	}
	r.start()
	ctx := r.awaitStream(t)
	last := lastWatchdogHealth(r.m.watchdog)
	require.Eventually(t, func() bool { return pendingWatchdogEvents(r.m) == 12 }, time.Second, time.Millisecond)
	require.Equal(t, last, lastWatchdogHealth(r.m.watchdog), "FAILED_PROCESSING_MUST_NOT_RESET: admission and blocked inspection are not health")
	r.clock.Advance(time.Minute)
	r.tick(t)
	if !awaitWatchdog(t, ctx.Done(), "WATCHDOG_MUST_CANCEL_OPEN_STREAM") {
		return
	}
	if !awaitWatchdog(t, r.done, "RUN_MUST_RETAIN_OWNED_JOIN") {
		return
	}
	<-joined
	require.ErrorIs(t, <-deliveryError, context.Canceled)
	require.Zero(t, pendingWatchdogEvents(r.m))
	require.Empty(t, r.m.discovery.events)
	r.m.discovery.mu.Lock()
	revision, generation := r.m.discovery.revision, r.m.discovery.generation
	r.m.discovery.mu.Unlock()
	require.EqualValues(t, 12, revision)
	require.EqualValues(t, 1, generation)
	require.Equal(t, last, lastWatchdogHealth(r.m.watchdog))
	require.Error(t, r.err)
}

type watchdogLogHandler struct {
	slog.Handler
	handle func(slog.Record)
}

func (h watchdogLogHandler) Handle(_ context.Context, r slog.Record) error { h.handle(r); return nil }
func watchRunLogs(m *Manager, handle func(slog.Record)) {
	m.logger = slog.New(watchdogLogHandler{Handler: slog.NewTextHandler(io.Discard, nil), handle: handle})
}

func TestManagerWatchdogCancelsBeforeBlockedLog(t *testing.T) {
	r := newWatchdogRun(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	watchRunLogs(r.m, func(record slog.Record) {
		if record.Message == "event stream watchdog detected failure, shutting down" {
			close(entered)
			<-release
		}
	})
	r.start()
	ctx := r.awaitStream(t)
	r.clock.Advance(time.Minute)
	r.tick(t)
	if !awaitWatchdog(t, entered, "terminal logger did not enter") {
		return
	}
	require.ErrorIs(t, ctx.Err(), context.Canceled, "ACTION_PRECEDES_LOGGING")
	select {
	case <-r.done:
		t.Error("RUN_MUST_RETAIN_OWNED_JOIN")
	default:
	}
	once.Do(func() { close(release) })
	if !awaitWatchdog(t, r.done, "RUN_MUST_RETAIN_OWNED_JOIN") {
		return
	}
	require.Error(t, r.err)
}

func TestManagerProbeAndAuditDoNotProvideHealth(t *testing.T) {
	r := newWatchdogRun(t)
	audited := make(chan struct{}, 8)
	pinged := make(chan struct{}, 8)
	r.m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryA}, nil }
	r.m.discovery.observe = func(context.Context, string) ([]int, error) { return nil, nil }
	r.m.discovery.after = func(d time.Duration) <-chan time.Time {
		select {
		case audited <- struct{}{}:
		default:
		}
		return time.After(d)
	}
	r.m.watchdog.dockerPing = watchdogPingFunc(func(context.Context) error { pinged <- struct{}{}; return nil })
	r.start()
	ctx := r.awaitStream(t)
	if !awaitWatchdog(t, audited, "audit must complete independently") {
		return
	}
	t0 := lastWatchdogHealth(r.m.watchdog)
	r.clock.Advance(30 * time.Second)
	r.tick(t)
	if !awaitWatchdog(t, pinged, "probe must run") {
		return
	}
	r.m.markRepair()
	if !awaitWatchdog(t, audited, "repair must complete independently") {
		return
	}
	require.Equal(t, t0, lastWatchdogHealth(r.m.watchdog), "successful inventory and Ping are not processing")
	r.clock.Advance(30 * time.Second)
	r.tick(t)
	if !awaitWatchdog(t, ctx.Done(), "WATCHDOG_MUST_CANCEL_OPEN_STREAM") {
		return
	}
	if !awaitWatchdog(t, r.done, "RUN_MUST_RETAIN_OWNED_JOIN") {
		return
	}
	require.Error(t, r.err)
}

func TestManagerWatchdogPreArmExits(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pre-canceled", true: "invalid-owner"}[closed], func(t *testing.T) {
			m, _, _ := recoveryManager(t)
			home, err := os.MkdirTemp("/tmp", "s10-pre-")
			require.NoError(t, err)
			defer os.RemoveAll(home)
			t.Setenv("HOME", home)
			owner, err := instancepath.Acquire(m.cfg.Host)
			require.NoError(t, err)
			defer owner.Close()
			if closed {
				require.NoError(t, owner.Close())
			}
			m.watchdog.ticks = func() (<-chan time.Time, func()) { t.Error("pre-arm exit started monitoring"); return nil, func() {} }
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = m.Run(ctx, owner)
			require.Error(t, err)
			require.False(t, m.watchdog.armed)
		})
	}
}

func TestManagerWatchdogExitPaths(t *testing.T) {
	for _, path := range []string{"ensure-gate", "ensure-backoff", "connectivity", "stream-backoff", "finish-stream"} {
		t.Run(path, func(t *testing.T) {
			r := newWatchdogRun(t)
			boundary := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			var gateDone chan error
			switch path {
			case "ensure-gate":
				entered := make(chan struct{})
				gateDone = make(chan error, 1)
				go func() {
					gateDone <- r.m.sshMaster.WithConnection(context.Background(), func(uint64, bool) error { close(entered); <-release; return nil })
				}()
				<-entered
				watchRunLogs(r.m, func(record slog.Record) {
					if record.Message == "Ensuring SSH ControlMaster is alive before starting event stream" {
						select {
						case boundary <- struct{}{}:
						default:
						}
					}
				})
			case "ensure-backoff":
				require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\nexit 7\n"), 0700))
				watchRunLogs(r.m, func(record slog.Record) {
					if record.Message == "SSH ControlMaster unavailable, retrying after backoff" {
						select {
						case boundary <- struct{}{}:
						default:
						}
					}
				})
			case "connectivity":
				file := filepath.Join(os.Getenv("PATH"), "connect-entered")
				t.Setenv("S10_CONNECT_ENTERED", file)
				require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\ncase \"$*\" in *docker\\ version*) printf entered > \"$S10_CONNECT_ENTERED\"; while :; do :; done;; esac\nexec \"$S10_TEST_EXECUTABLE\" -test.run '^TestWatchdogNativeSSH$' -- \"$@\"\n"), 0700))
			case "stream-backoff", "finish-stream":
				r.m.discovery.openStream = func(ctx context.Context, _ func(context.Context, docker.Event) error) <-chan docker.StreamResult {
					terminal := make(chan docker.StreamResult, 1)
					r.stream <- ctx
					if path == "finish-stream" {
						r.m.discovery.mu.Lock()
						r.m.discovery.generation++
						r.m.discovery.mu.Unlock()
					}
					terminal <- docker.StreamResult{Err: errors.New("owned terminal error")}
					close(terminal)
					return terminal
				}
				watchRunLogs(r.m, func(record slog.Record) {
					if record.Message == "event stream error, restarting after backoff" {
						select {
						case boundary <- struct{}{}:
						default:
						}
					}
				})
			}
			r.start()
			if path == "finish-stream" {
				// Startup has its existing three-second harness guard; the one-second
				// ownership assertion starts only after this scenario reaches a stream.
				select {
				case <-r.stream:
				case <-r.done:
					// A fast ordinary return and the buffered receipt can both be ready.
					select {
					case <-r.stream:
					default:
						t.Fatalf("Run returned before finish-stream boundary: %v", r.err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("owned finish-stream boundary did not enter")
				}
				if !awaitWatchdog(t, r.done, "ordinary return must join watchdog owner") {
					return
				}
				require.ErrorContains(t, r.err, "unretired event reservations")
				require.True(t, r.m.stopping.Load())
				<-r.stopped
				return
			}
			if path == "connectivity" {
				require.Eventually(t, func() bool { _, err := os.Stat(os.Getenv("S10_CONNECT_ENTERED")); return err == nil }, time.Second, time.Millisecond)
			} else if !awaitWatchdog(t, boundary, "requested Run boundary not entered") {
				return
			}
			r.clock.Advance(time.Minute)
			r.tick(t)
			require.Eventually(t, func() bool { return r.m.stopping.Load() }, time.Second, time.Millisecond, "WATCHDOG_MUST_CANCEL_OPEN_STREAM")
			if path == "ensure-gate" {
				select {
				case <-r.done:
					t.Error("gate must still be held")
				default:
				}
			}
			releaseOnce.Do(func() { close(release) })
			if gateDone != nil {
				require.NoError(t, <-gateDone)
			}
			if !awaitWatchdog(t, r.done, "RUN_MUST_RETAIN_OWNED_JOIN") {
				return
			}
			require.ErrorContains(t, r.err, "no successfully handled Docker events", "WATCHDOG_CAUSE_MUST_SURVIVE")
			<-r.stopped
			if path == "connectivity" {
				require.Empty(t, r.stream, "terminal preflight must not open a replacement stream")
			}
		})
	}
}

// Retain the production retry schedule: this is deliberately a ~151s test,
// not a retry-policy seam or a claim that the fake processing clock is real.
func TestManagerWatchdogRestartLimitJoinsProbe(t *testing.T) {
	if testing.Short() {
		t.Skip("production retry exhaustion takes ~151s; mandatory full no-short native and Quality profiles cover it")
	}
	r := newWatchdogRun(t)
	attempts := make(chan int, 10)
	probeEntered := make(chan context.Context, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	r.m.watchdog.dockerPing = watchdogPingFunc(func(ctx context.Context) error { probeEntered <- ctx; <-release; return ctx.Err() })
	count := 0
	r.m.discovery.openStream = func(context.Context, func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		count++
		attempts <- count
		done := make(chan docker.StreamResult, 1)
		done <- docker.StreamResult{Err: errors.New("owned stream failed")}
		close(done)
		return done
	}
	r.start()
	require.Equal(t, 1, <-attempts)
	r.clock.Advance(30 * time.Second)
	r.tick(t)
	probe := <-probeEntered
	for want := 2; want <= 10; want++ {
		select {
		case got := <-attempts:
			require.Equal(t, want, got)
		case <-time.After(35 * time.Second):
			t.Fatal("production retry did not reach next attempt")
		}
	}
	require.Eventually(t, func() bool { return r.m.stopping.Load() }, time.Second, time.Millisecond)
	cause, fatal := r.m.watchdog.terminalResult()
	require.False(t, fatal)
	require.ErrorContains(t, cause, "event stream failed after 10 consecutive failures")
	require.Error(t, probe.Err())
	select {
	case <-r.done:
		t.Error("RUN_MUST_RETAIN_OWNED_JOIN")
	case <-time.After(200 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	if !awaitWatchdog(t, r.done, "ordinary terminal must join") {
		return
	}
	require.Same(t, cause, r.err)
}

func TestManagerWatchdogParentCancelPreflightCleanup(t *testing.T) {
	r := newWatchdogRun(t)
	entered := filepath.Join(os.Getenv("PATH"), "parent-preflight-entered")
	t.Setenv("S10_CONNECT_ENTERED", entered)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\ncase \"$*\" in *docker\\ version*) printf entered > \"$S10_CONNECT_ENTERED\"; while :; do :; done;; esac\nexec \"$S10_TEST_EXECUTABLE\" -test.run '^TestWatchdogNativeSSH$' -- \"$@\"\n"), 0700))
	r.start()
	require.Eventually(t, func() bool { _, err := os.Stat(entered); return err == nil }, time.Second, time.Millisecond)
	r.m.state.SetDesired(discoveryA, []int{18881})
	r.m.state.MarkActive(discoveryA, 18881)
	r.cancel()
	if !awaitWatchdog(t, r.done, "PARENT_PREFLIGHT_MUST_JOIN") {
		return
	}
	require.NoError(t, r.err, "parent cancellation retains nil convention")
	require.Empty(t, r.stream, "parent-canceled preflight must not open replacement")
	desired := r.m.state.GetDesired()
	require.Len(t, desired, 1)
	require.Equal(t, discoveryA, desired[0].ContainerID)
	require.Empty(t, desired[0].Ports, "PARENT_PREFLIGHT_MUST_CLEAN_DESIRED")
	require.Empty(t, r.m.state.GetActual(), "PARENT_PREFLIGHT_MUST_CLEAN_ACTUAL")
	shutdownRecords := 0
	for _, entry := range r.m.history.GetAll() {
		if entry.EndReason == "rdhpf shutdown" {
			shutdownRecords++
		}
	}
	require.Equal(t, 1, shutdownRecords, "cleanup must run exactly once; reconciliation also records container stopped")
	_, fatal := r.m.watchdog.terminalResult()
	require.False(t, fatal)
}

func TestWatchdogRunMasterRetiredBeforeRuntime(t *testing.T) {
	r := newWatchdogRun(t)
	home := os.Getenv("HOME")
	r.start()
	r.awaitStream(t)
	require.NoError(t, r.m.sshMaster.Check(), "real retained native Master initially ready")
	r.beforeRuntimeRetirement = func() {
		_, err := os.Stat(home)
		require.NoError(t, err, "runtime remains until owned retirement")
		peer, err := instancepath.Acquire(r.m.cfg.Host)
		if peer != nil {
			require.NoError(t, peer.Close())
		}
		require.Error(t, err, "admission remains held until owned retirement")
		require.Error(t, r.m.sshMaster.Check(), "MASTER_MUST_JOIN_BEFORE_RUNTIME_RETIREMENT")
		_, err = os.Stat(filepath.Dir(r.m.sshMaster.ControlPath()))
		require.True(t, os.IsNotExist(err), "retained Wait and namespace close precede runtime retirement")
	}
}
