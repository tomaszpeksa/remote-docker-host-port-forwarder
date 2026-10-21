package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
)

func TestManagerStreamRestartPacing(t *testing.T) {
	r := newWatchdogRun(t)
	launches := make(chan time.Time, 4)
	calls := 0 // Run is the sole stream opener.
	r.m.discovery.openStream = func(ctx context.Context, _ func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		calls++
		launches <- time.Now()
		terminal := make(chan docker.StreamResult, 1)
		if calls <= 3 {
			terminal <- docker.StreamResult{}
			close(terminal)
		} else {
			go func() {
				<-ctx.Done()
				terminal <- docker.StreamResult{Canceled: true}
				close(terminal)
			}()
		}
		return terminal
	}
	r.start()
	previous := awaitStreamLaunch(t, launches)
	for range 3 {
		next := awaitStreamLaunch(t, launches)
		require.GreaterOrEqual(t, next.Sub(previous), time.Second, "CLEAN_EOF_MUST_PACE")
		previous = next
	}
}

func TestManagerStreamRestartCancellation(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent", true: "watchdog"}[fatal], func(t *testing.T) {
			r := newWatchdogRun(t)
			// Inventory must not retire the manually seeded cleanup subject.
			r.m.discovery.list = func(ctx context.Context) ([]string, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			armed, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var logsMu sync.Mutex
			var logs []string
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			r.m.logger = slog.New(watchdogLogHandler{
				Handler: slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}),
				handle: func(record slog.Record) {
					var attrs []slog.Attr
					record.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
					logsMu.Lock()
					logs = append(logs, fmt.Sprintf("%s %v", record.Message, attrs))
					logsMu.Unlock()
					if record.Message == "clean event stream ended, waiting before replacement" {
						close(armed)
						<-release
					}
				},
			})
			calls := 0
			r.m.discovery.openStream = func(ctx context.Context, _ func(context.Context, docker.Event) error) <-chan docker.StreamResult {
				calls++
				r.stream <- ctx
				terminal := make(chan docker.StreamResult, 1)
				terminal <- docker.StreamResult{}
				close(terminal)
				return terminal
			}
			r.start()
			r.awaitStream(t)
			if !awaitWatchdog(t, armed, "EOF_TIMER_MUST_ARM") {
				return
			}
			// This desired owner has no reconciliation request: terminal cleanup,
			// rather than an audit, must retire it once after the wait is canceled.
			r.m.state.SetDesired(discoveryA, []int{18881})
			r.m.state.MarkActive(discoveryA, 18881)
			if fatal {
				r.clock.Advance(time.Minute)
				r.tick(t)
			} else {
				r.cancel()
			}
			once.Do(func() { close(release) })
			if !awaitWatchdog(t, r.done, "EOF_WAIT_MUST_CANCEL_AND_JOIN") {
				return
			}
			logsMu.Lock()
			captured := append([]string(nil), logs...)
			logsMu.Unlock()
			for _, line := range captured {
				t.Log(line)
			}
			cause, watchdogFailure := r.m.watchdog.terminalResult()
			t.Logf("joined Run: desired=%+v actual=%+v history=%+v cause=%v watchdog=%v", r.m.state.GetDesired(), r.m.state.GetActual(), r.m.history.GetAll(), cause, watchdogFailure)
			require.Equal(t, 1, calls, "CANCELED_EOF_MUST_NOT_REPLACE")
			if fatal {
				require.ErrorContains(t, r.err, "no successfully handled Docker events")
			} else {
				require.NoError(t, r.err)
			}
			require.Empty(t, r.m.state.GetActual(), "EOF_CANCEL_MUST_CLEAN_FORWARDS")
			shutdown := 0
			for _, entry := range r.m.history.GetAll() {
				if entry.EndReason == "rdhpf shutdown" {
					shutdown++
				}
			}
			require.Equal(t, 1, shutdown)
		})
	}
}

func TestManagerStreamRestartHistory(t *testing.T) {
	r := newWatchdogRun(t)
	sshPath := filepath.Join(os.Getenv("PATH"), "ssh")
	original, err := os.ReadFile(sshPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sshPath, []byte("#!/bin/sh\nexit 7\n"), 0700))
	restored := make(chan error, 1)
	var mu sync.Mutex
	var delays []time.Duration
	watchRunLogs(r.m, func(record slog.Record) {
		if record.Message == "SSH ControlMaster unavailable, retrying after backoff" {
			restored <- os.WriteFile(sshPath, original, 0700)
		}
		if record.Message == "SSH ControlMaster unavailable, retrying after backoff" || record.Message == "event stream error, restarting after backoff" {
			record.Attrs(func(a slog.Attr) bool {
				if a.Key == "backoff_delay" {
					mu.Lock()
					delays = append(delays, a.Value.Duration())
					mu.Unlock()
				}
				return true
			})
		}
	})
	launches := make(chan time.Time, 4)
	calls := 0
	r.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		calls++
		launches <- time.Now()
		terminal := make(chan docker.StreamResult, 1)
		if calls <= 3 {
			// Drive a real successful stop handler before both error endings.
			_ = deliver(ctx, docker.Event{Type: "stop", ContainerID: discoveryA})
			result := docker.StreamResult{}
			if calls != 2 {
				result.Err = errors.New("owned stream error")
			}
			terminal <- result
			close(terminal)
		} else {
			go func() {
				<-ctx.Done()
				terminal <- docker.StreamResult{Canceled: true}
				close(terminal)
			}()
		}
		return terminal
	}
	r.start()
	previous := awaitStreamLaunch(t, launches)
	require.NoError(t, <-restored)
	for _, delay := range []time.Duration{2 * time.Second, time.Second, time.Second} {
		next := awaitStreamLaunch(t, launches)
		require.GreaterOrEqual(t, next.Sub(previous), delay)
		previous = next
	}
	mu.Lock()
	got := append([]time.Duration(nil), delays...)
	mu.Unlock()
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second, time.Second}, got, "EOF_FORGIVES_BUT_HANDLING_DOES_NOT")
}

func awaitStreamLaunch(t *testing.T, launches <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-launches:
		return at
	case <-time.After(5 * time.Second):
		t.Fatal("stream replacement did not enter")
	}
	return time.Time{}
}
