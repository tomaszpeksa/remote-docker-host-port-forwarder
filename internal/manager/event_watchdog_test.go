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

func TestEventWatchdogBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name         string
		elapsed      time.Duration
		probe, fatal bool
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
			clock.Advance(tc.elapsed)
			deadline, err := w.Tick()
			if tc.probe {
				require.Equal(t, time.Unix(1060, 0), deadline)
			} else {
				require.True(t, deadline.IsZero())
			}
			if tc.fatal {
				require.ErrorContains(t, err, "no successfully handled Docker events")
				require.ErrorIs(t, ctx.Err(), context.Canceled)
			} else {
				require.NoError(t, err)
				require.NoError(t, ctx.Err())
			}
		})
	}
}

func TestEventWatchdogArmsAtRunOnlyOnce(t *testing.T) {
	clock := newFakeClock(time.Unix(1000, 0))
	w := newEventWatchdog(clock.Now, nil)
	clock.Advance(2 * time.Minute)
	w.OnEvent()
	deadline, err := w.Tick()
	require.NoError(t, err)
	require.True(t, deadline.IsZero())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.arm(context.Background(), cancel, &atomic.Bool{})
	deadline, err = w.Tick()
	require.NoError(t, err)
	require.True(t, deadline.IsZero(), "construction age is not processing silence")
	clock.Advance(40 * time.Second)
	w.arm(context.Background(), cancel, &atomic.Bool{})
	deadline, err = w.Tick()
	require.NoError(t, err)
	require.Equal(t, time.Unix(1180, 0), deadline, "RETRY_MUST_NOT_REARM")
	clock.Advance(20 * time.Second)
	_, err = w.Tick()
	require.Error(t, err)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
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
			ordinary := errors.New("ordinary terminal error")
			clock.Advance(time.Minute)
			switch order {
			case "health-before-check":
				w.OnEvent()
				deadline, err := w.Tick()
				require.NoError(t, err)
				require.True(t, deadline.IsZero())
				require.False(t, stopping.Load())
				return
			case "fatal-before-health":
				_, err := w.Tick()
				require.Error(t, err)
				clock.Advance(time.Second)
				w.OnEvent()
				require.Equal(t, time.Unix(1000, 0), w.lastEvent)
				require.Same(t, err, w.finish(ordinary), "WATCHDOG_CAUSE_MUST_SURVIVE")
			case "parent-before-fatal":
				parentCancel()
				_, err := w.Tick()
				require.NoError(t, err)
				require.NoError(t, w.finish(ordinary))
			case "fatal-before-parent":
				_, err := w.Tick()
				parentCancel()
				require.Same(t, err, w.finish(ordinary), "WATCHDOG_CAUSE_MUST_SURVIVE")
			case "ordinary-before-fatal":
				require.Same(t, ordinary, w.finish(ordinary))
				_, err := w.Tick()
				require.Same(t, ordinary, err)
			case "nil-before-fatal":
				require.NoError(t, w.finish(nil))
				_, err := w.Tick()
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
				time.Sleep(30 * time.Second)
				ticks <- t0
				synctest.Wait()
				require.ErrorIs(t, ctx.Err(), context.Canceled)
				err, fatal := w.terminalResult()
				require.True(t, fatal)
				require.Error(t, err)
			})
		})
	}
}

func TestEventWatchdogLoopDiagnosticOrigin(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-before-tick", true: "watchdog"}[fatal], func(t *testing.T) {
			clock := newFakeClock(time.Unix(1000, 0))
			w, ctx := armWatchdog(t, clock)
			m := &Manager{watchdog: w}
			var headlines []string
			watchRunLogs(m, func(record slog.Record) { headlines = append(headlines, record.Message) })
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
			} else {
				require.Empty(t, headlines, "ORDINARY_CAUSE_MUST_NOT_LOG_WATCHDOG_FAILURE")
				cause, origin := w.terminalResult()
				require.Same(t, ordinary, cause)
				require.False(t, origin)
			}
		})
	}
}
