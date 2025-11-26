package manager

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fakeClock provides a controllable time source for testing
type fakeClock struct {
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t}
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

// fakePingRunner provides a controllable ping runner for testing
type fakePingRunner struct {
	calls     int
	shouldErr bool
	err       error
}

func (f *fakePingRunner) Ping(ctx context.Context) error {
	f.calls++
	if f.shouldErr {
		if f.err != nil {
			return f.err
		}
		return fmt.Errorf("fake ping error")
	}
	return nil
}

func TestEventWatchdog_HealthyUnder30s(t *testing.T) {
	clock := newFakeClock(time.Now())
	ping := &fakePingRunner{}
	watchdog := newEventWatchdog(clock.Now, ping)

	// Advance time by 29 seconds
	clock.Advance(29 * time.Second)

	// Tick should return nil (healthy), no ping should be called
	err := watchdog.Tick(context.Background())
	if err != nil {
		t.Errorf("Expected nil error, got: %v", err)
	}
	if ping.calls != 0 {
		t.Errorf("Expected 0 ping calls, got: %d", ping.calls)
	}
}

func TestEventWatchdog_PingsBetween30And60s(t *testing.T) {
	clock := newFakeClock(time.Now())
	ping := &fakePingRunner{}
	watchdog := newEventWatchdog(clock.Now, ping)

	// Advance time by 30 seconds (idle threshold)
	clock.Advance(30 * time.Second)

	// First tick should trigger ping
	err := watchdog.Tick(context.Background())
	if err != nil {
		t.Errorf("Expected nil error at 30s, got: %v", err)
	}
	if ping.calls != 1 {
		t.Errorf("Expected 1 ping call at 30s, got: %d", ping.calls)
	}

	// Advance to 45 seconds
	clock.Advance(15 * time.Second)

	// Second tick should also trigger ping (still in idle window)
	err = watchdog.Tick(context.Background())
	if err != nil {
		t.Errorf("Expected nil error at 45s, got: %v", err)
	}
	if ping.calls != 2 {
		t.Errorf("Expected 2 ping calls at 45s, got: %d", ping.calls)
	}
}

func TestEventWatchdog_FatalAt60s(t *testing.T) {
	clock := newFakeClock(time.Now())
	ping := &fakePingRunner{}
	watchdog := newEventWatchdog(clock.Now, ping)

	// Advance time by 60 seconds (fatal threshold)
	clock.Advance(60 * time.Second)

	// Tick should return fatal error
	err := watchdog.Tick(context.Background())
	if err == nil {
		t.Error("Expected fatal error at 60s, got nil")
	}
	if err != nil && err.Error() != "event stream unhealthy: no events for 1m0s" {
		t.Errorf("Unexpected error message: %v", err)
	}
}

func TestEventWatchdog_OnEventResetsTimer(t *testing.T) {
	clock := newFakeClock(time.Now())
	ping := &fakePingRunner{}
	watchdog := newEventWatchdog(clock.Now, ping)

	// Advance to 40 seconds (should trigger ping)
	clock.Advance(40 * time.Second)
	err := watchdog.Tick(context.Background())
	if err != nil {
		t.Errorf("Expected nil error at 40s, got: %v", err)
	}
	if ping.calls != 1 {
		t.Errorf("Expected 1 ping call, got: %d", ping.calls)
	}

	// Simulate receiving an event - this should reset the timer
	watchdog.OnEvent()

	// Now advance only 29 seconds from the event
	clock.Advance(29 * time.Second)

	// Tick should NOT ping (we're under 30s since last event)
	err = watchdog.Tick(context.Background())
	if err != nil {
		t.Errorf("Expected nil error after reset, got: %v", err)
	}
	if ping.calls != 1 {
		t.Errorf("Expected still 1 ping call after reset, got: %d", ping.calls)
	}
}

func TestEventWatchdog_PingErrorsNotImmediatelyFatal(t *testing.T) {
	clock := newFakeClock(time.Now())
	ping := &fakePingRunner{shouldErr: true}
	watchdog := newEventWatchdog(clock.Now, ping)

	// Advance to 35 seconds (in ping window)
	clock.Advance(35 * time.Second)

	// Tick should call ping, but error from ping should not be fatal
	err := watchdog.Tick(context.Background())
	if err != nil {
		t.Errorf("Expected nil error despite ping failure, got: %v", err)
	}
	if ping.calls != 1 {
		t.Errorf("Expected 1 ping call, got: %d", ping.calls)
	}

	// Advance to 59 seconds (still not fatal)
	clock.Advance(24 * time.Second)
	err = watchdog.Tick(context.Background())
	if err != nil {
		t.Errorf("Expected nil error at 59s even with failing pings, got: %v", err)
	}

	// Advance to 60 seconds - NOW it should be fatal because of time
	clock.Advance(1 * time.Second)
	err = watchdog.Tick(context.Background())
	if err == nil {
		t.Error("Expected fatal error at 60s, got nil")
	}
}

func TestEventWatchdog_MultipleEventsKeepHealthy(t *testing.T) {
	clock := newFakeClock(time.Now())
	ping := &fakePingRunner{}
	watchdog := newEventWatchdog(clock.Now, ping)

	// Simulate events every 20 seconds for 2 minutes
	for i := 0; i < 6; i++ {
		clock.Advance(20 * time.Second)
		watchdog.OnEvent()

		// Tick should never trigger ping or error
		err := watchdog.Tick(context.Background())
		if err != nil {
			t.Errorf("Iteration %d: Expected nil error, got: %v", i, err)
		}
		if ping.calls != 0 {
			t.Errorf("Iteration %d: Expected 0 ping calls, got: %d", i, ping.calls)
		}
	}
}