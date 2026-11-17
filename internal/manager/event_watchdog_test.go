package manager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock  { return &fakeClock{now: now} }
func (c *fakeClock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }

func armWatchdog(t *testing.T, clock *fakeClock) (*eventWatchdog, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := newEventWatchdog(clock.Now, nil)
	w.arm(context.Background(), cancel, &atomic.Bool{})
	return w, ctx
}

// watchTestStream registers a fresh stream with the watchdog, as Run does
// before each subscription, and returns its context.
func watchTestStream(t *testing.T, w *eventWatchdog) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.watchStream(cancel)
	return ctx
}

// exhaustWatchdogRestarts makes the next stall escalate, as if the previous
// restarts had not restored event processing.
func exhaustWatchdogRestarts(w *eventWatchdog) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.restarts = maxWatchdogStreamRestarts
}

func TestEventWatchdogBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name           string
		elapsed        time.Duration
		probe, restart bool
	}{
		{"under30", 30*time.Second - time.Nanosecond, false, false},
		{"at30", 30 * time.Second, true, false},
		{"under60", 60*time.Second - time.Nanosecond, true, false},
		{"at60", 60 * time.Second, false, true},
		{"after60", 61 * time.Second, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t0 := time.Unix(1000, 0)
			clock := newFakeClock(t0)
			w, ctx := armWatchdog(t, clock)
			stream := watchTestStream(t, w)
			clock.Advance(tc.elapsed)
			deadline, restart, err := w.Tick()
			require.NoError(t, err)
			require.NoError(t, ctx.Err(), "a stall never shuts down while restarts remain")
			if tc.probe {
				require.Equal(t, time.Unix(1060, 0), deadline)
			} else {
				require.True(t, deadline.IsZero())
			}
			if tc.restart {
				require.Equal(t, 1, restart)
				require.ErrorIs(t, stream.Err(), context.Canceled)
			} else {
				require.Zero(t, restart)
				require.NoError(t, stream.Err())
			}
		})
	}
}

func TestEventWatchdogStallRestartsStream(t *testing.T) {
	t0 := time.Unix(1000, 0)
	clock := newFakeClock(t0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping := &atomic.Bool{}
	w := newEventWatchdog(clock.Now, nil)
	w.arm(context.Background(), cancel, stopping)
	first := watchTestStream(t, w)

	clock.Advance(60 * time.Second)
	deadline, restart, err := w.Tick()
	require.NoError(t, err)
	require.True(t, deadline.IsZero())
	require.Equal(t, 1, restart)
	require.ErrorIs(t, first.Err(), context.Canceled, "the silent stream is restarted")
	require.NoError(t, ctx.Err(), "Run keeps going: forwards stay up")
	require.False(t, stopping.Load())
	cause, fatal := w.terminalResult()
	require.NoError(t, cause)
	require.False(t, fatal)

	second := watchTestStream(t, w)
	_, restart, err = w.Tick()
	require.NoError(t, err)
	require.Zero(t, restart, "the restart opens a fresh silence window")
	clock.Advance(30 * time.Second)
	deadline, restart, err = w.Tick()
	require.NoError(t, err)
	require.Zero(t, restart)
	require.Equal(t, time.Unix(1120, 0), deadline, "probe allowance counts from the restart")
	require.NoError(t, second.Err())
	require.Equal(t, t0, lastWatchdogHealth(w), "a restart is not processing health")
}

func TestEventWatchdogEscalatesAfterFailedRestarts(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopping := &atomic.Bool{}
	w := newEventWatchdog(clock.Now, nil)
	w.arm(context.Background(), cancel, stopping)
	for want := 1; want <= maxWatchdogStreamRestarts; want++ {
		stream := watchTestStream(t, w)
		clock.Advance(60 * time.Second)
		_, restart, err := w.Tick()
		require.NoError(t, err)
		require.Equal(t, want, restart)
		require.ErrorIs(t, stream.Err(), context.Canceled)
		require.NoError(t, ctx.Err())
	}
	last := watchTestStream(t, w)
	clock.Advance(60 * time.Second)
	_, restart, err := w.Tick()
	require.Zero(t, restart)
	require.EqualError(t, err, "event stream unhealthy: no successfully handled Docker events for 4m0s after 3 event stream restarts")
	require.ErrorIs(t, ctx.Err(), context.Canceled, "repeated failed restarts escalate to shutdown")
	require.True(t, stopping.Load())
	cause, fatal := w.terminalResult()
	require.Same(t, err, cause)
	require.True(t, fatal)
	require.NoError(t, last.Err(), "escalation cancels Run, which owns the stream")
}

func TestEventWatchdogSuccessResetsRestarts(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	w, ctx := armWatchdog(t, clock)
	for range maxWatchdogStreamRestarts {
		watchTestStream(t, w)
		clock.Advance(60 * time.Second)
		_, _, err := w.Tick()
		require.NoError(t, err)
	}
	w.OnEvent()
	for want := 1; want <= maxWatchdogStreamRestarts; want++ {
		watchTestStream(t, w)
		clock.Advance(60 * time.Second)
		_, restart, err := w.Tick()
		require.NoError(t, err)
		require.Equal(t, want, restart, "a successful event resets the restart count")
	}
	require.NoError(t, ctx.Err())
	clock.Advance(60 * time.Second)
	_, _, err := w.Tick()
	require.ErrorContains(t, err, "for 4m0s after 3 event stream restarts", "silence counts from the last successful event")
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestEventWatchdogArmsAtRunOnlyOnce(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	w := newEventWatchdog(clock.Now, nil)
	clock.Advance(2 * time.Minute)
	w.OnEvent()
	deadline, _, err := w.Tick()
	require.NoError(t, err)
	require.True(t, deadline.IsZero())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.arm(context.Background(), cancel, &atomic.Bool{})
	deadline, _, err = w.Tick()
	require.NoError(t, err)
	require.True(t, deadline.IsZero(), "construction age is not processing silence")
	clock.Advance(40 * time.Second)
	w.arm(context.Background(), cancel, &atomic.Bool{})
	deadline, _, err = w.Tick()
	require.NoError(t, err)
	require.Equal(t, time.Unix(1180, 0), deadline, "RETRY_MUST_NOT_REARM")
	clock.Advance(20 * time.Second)
	_, restart, err := w.Tick()
	require.NoError(t, err)
	require.Equal(t, 1, restart)
	require.NoError(t, ctx.Err())
}

func TestEventWatchdogFirstTerminalWins(t *testing.T) {
	for _, order := range []string{"health-before-check", "fatal-before-health", "parent-before-fatal", "fatal-before-parent", "ordinary-before-fatal", "nil-before-fatal"} {
		t.Run(order, func(t *testing.T) {
			clock := newFakeClock(time.Unix(1000, 0))
			parent, parentCancel := context.WithCancel(context.Background())
			defer parentCancel()
			ctx, cancel := context.WithCancel(parent)
			defer cancel()
			stopping := &atomic.Bool{}
			w := newEventWatchdog(clock.Now, nil)
			w.arm(parent, cancel, stopping)
			exhaustWatchdogRestarts(w)
			ordinary := errors.New("ordinary terminal error")
			clock.Advance(time.Minute)
			switch order {
			case "health-before-check":
				w.OnEvent()
				deadline, _, err := w.Tick()
				require.NoError(t, err)
				require.True(t, deadline.IsZero())
				require.False(t, stopping.Load())
				return
			case "fatal-before-health":
				_, _, err := w.Tick()
				require.Error(t, err)
				clock.Advance(time.Second)
				w.OnEvent()
				require.Equal(t, time.Unix(1000, 0), w.lastEvent)
				require.Same(t, err, w.finish(ordinary), "WATCHDOG_CAUSE_MUST_SURVIVE")
			case "parent-before-fatal":
				parentCancel()
				_, _, err := w.Tick()
				require.NoError(t, err)
				require.NoError(t, w.finish(ordinary))
			case "fatal-before-parent":
				_, _, err := w.Tick()
				parentCancel()
				require.Same(t, err, w.finish(ordinary), "WATCHDOG_CAUSE_MUST_SURVIVE")
			case "ordinary-before-fatal":
				require.Same(t, ordinary, w.finish(ordinary))
				_, _, err := w.Tick()
				require.Same(t, ordinary, err)
			case "nil-before-fatal":
				require.NoError(t, w.finish(nil))
				_, _, err := w.Tick()
				require.NoError(t, err)
				require.NoError(t, w.finish(ordinary))
			}
			require.True(t, stopping.Load())
			require.ErrorIs(t, ctx.Err(), context.Canceled)
		})
	}
}

func TestEventWatchdogProbeRemainingAllowance(t *testing.T) {
	for _, parentBound := range []bool{false, true} {
		t.Run(map[bool]string{false: "health-does-not-extend", true: "parent-shortens"}[parentBound], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				t0 := time.Now()
				parent := context.Background()
				parentCancel := func() {}
				if parentBound {
					parent, parentCancel = context.WithDeadline(parent, t0.Add(45*time.Second))
				}
				defer parentCancel()
				ctx, cancel := context.WithCancel(parent)
				defer cancel()
				entered := make(chan context.Context, 2)
				release := make(chan struct{})
				var once sync.Once
				w := newEventWatchdog(time.Now, watchdogPingFunc(func(ctx context.Context) error { entered <- ctx; <-release; return errors.New("probe failed") }))
				w.arm(parent, cancel, &atomic.Bool{})
				m := &Manager{watchdog: w, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
				ticks := make(chan time.Time)
				done := make(chan struct{})
				go func() { defer close(done); m.startEventWatchdogLoop(ctx, ticks) }()
				defer func() { cancel(); once.Do(func() { close(release) }); <-done }()
				time.Sleep(30 * time.Second)
				ticks <- t0
				probe := <-entered
				deadline, ok := probe.Deadline()
				require.True(t, ok)
				want := t0.Add(60 * time.Second)
				if parentBound {
					want = t0.Add(45 * time.Second)
				}
				require.Equal(t, want, deadline, "PROBE_USES_REMAINING_ALLOWANCE")
				time.Sleep(10 * time.Second)
				w.OnEvent()
				ticks <- t0
				synctest.Wait()
				require.NoError(t, probe.Err(), "no independent five-second cap")
				time.Sleep(time.Until(want))
				synctest.Wait()
				require.ErrorIs(t, probe.Err(), context.DeadlineExceeded)
				if !parentBound {
					require.NoError(t, ctx.Err())
					ticks <- t0
					synctest.Wait()
					require.NoError(t, ctx.Err(), "late successful health postpones next fatal check, not old attempt deadline")
				}
				select {
				case <-entered:
					t.Error("overlapping probe")
				default:
				}
				select {
				case <-done:
					t.Error("RUN_MUST_RETAIN_OWNED_JOIN")
				default:
				}
				once.Do(func() { close(release) })
				synctest.Wait()
				if parentBound {
					return
				}
				select {
				case <-entered:
					t.Error("completion must not immediately retry")
				default:
				}
				time.Sleep(10 * time.Second)
				ticks <- t0
				next := <-entered
				nextDeadline, _ := next.Deadline()
				require.Equal(t, t0.Add(100*time.Second), nextDeadline)
				synctest.Wait()
				require.NoError(t, ctx.Err(), "probe error is not fatal or health")
				stream := watchTestStream(t, w)
				time.Sleep(30 * time.Second)
				ticks <- t0
				synctest.Wait()
				require.ErrorIs(t, stream.Err(), context.Canceled, "a stall restarts the stream")
				require.NoError(t, ctx.Err(), "a stall with restarts left does not shut down")
				err, fatal := w.terminalResult()
				require.False(t, fatal)
				require.NoError(t, err)
			})
		})
	}
}

func TestEventWatchdogLoopDiagnosticOrigin(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-before-tick", true: "watchdog"}[fatal], func(t *testing.T) {
			clock := newFakeClock(time.Unix(1000, 0))
			w, ctx := armWatchdog(t, clock)
			exhaustWatchdogRestarts(w)
			m := &Manager{watchdog: w}
			var headlines []string
			var reason string
			watchRunLogs(m, func(record slog.Record) {
				headlines = append(headlines, record.Message)
				reason = logAttr(record, "reason")
			})
			ticks, done := make(chan time.Time), make(chan struct{})
			ordinary := errors.New("ordinary Run terminal")
			clock.Advance(time.Minute)
			w.mu.Lock()
			go func() { defer close(done); m.startEventWatchdogLoop(ctx, ticks) }()
			ticks <- time.Unix(0, 0) // loop has selected tick, but Tick still waits for the latch
			if !fatal {
				w.selectTerminalLocked(ordinary, false)
			}
			w.mu.Unlock()
			<-done
			if fatal {
				require.Equal(t, []string{"event stream watchdog detected failure, shutting down"}, headlines)
				require.Equal(t, "event stream restarts did not restore event processing", reason)
			} else {
				require.Empty(t, headlines, "ORDINARY_CAUSE_MUST_NOT_LOG_WATCHDOG_FAILURE")
				cause, origin := w.terminalResult()
				require.Same(t, ordinary, cause)
				require.False(t, origin)
			}
		})
	}
}

func logAttr(record slog.Record, key string) string {
	var value string
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == key {
			value = attr.Value.String()
			return false
		}
		return true
	})
	return value
}

func TestEventWatchdogLoopLogsRestart(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	w, ctx := armWatchdog(t, clock)
	stream := watchTestStream(t, w)
	m := &Manager{watchdog: w}
	type logged struct{ message, reason, restart string }
	records := make(chan logged, 4)
	watchRunLogs(m, func(record slog.Record) {
		records <- logged{record.Message, logAttr(record, "reason"), logAttr(record, "restart")}
	})
	ticks, done := make(chan time.Time), make(chan struct{})
	loopCtx, stop := context.WithCancel(ctx)
	go func() { defer close(done); m.startEventWatchdogLoop(loopCtx, ticks) }()
	clock.Advance(time.Minute)
	ticks <- time.Unix(0, 0)
	require.Equal(t, logged{"event stream watchdog restarting silent event stream", "no successfully handled Docker events", "1"}, <-records)
	require.ErrorIs(t, stream.Err(), context.Canceled)
	stop()
	<-done
	require.NoError(t, ctx.Err(), "the loop keeps Run alive after a restart")
}
