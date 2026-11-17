package manager

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// stallStream advances the processing clock past the silence window, delivers
// one watchdog check, and returns the replacement stream Run opens.
func (r *watchdogRun) stallStream(t *testing.T, current context.Context) context.Context {
	t.Helper()
	r.clock.Advance(60 * time.Second)
	r.tick(t)
	if !awaitWatchdog(t, current.Done(), "WATCHDOG_MUST_RESTART_SILENT_STREAM") {
		t.FailNow()
	}
	next := r.awaitStream(t)
	require.NoError(t, next.Err())
	require.False(t, r.m.stopping.Load(), "STREAM_RESTART_MUST_NOT_SHUT_DOWN")
	return next
}

// runningInventory serves a mutable container/port inventory to discovery.
type runningInventory struct {
	mu    sync.Mutex
	ports map[string][]int
}

func (i *runningInventory) set(ports map[string][]int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ports = ports
}

func (i *runningInventory) list(context.Context) ([]string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	ids := make([]string, 0, len(i.ports))
	for id := range i.ports {
		ids = append(ids, id)
	}
	return ids, nil
}

func (i *runningInventory) observe(_ context.Context, id string) ([]int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]int{}, i.ports[id]...), nil
}

func activeForward(m *Manager, id string, port int) (state.ForwardState, bool) {
	for _, forward := range m.state.GetByContainer(id) {
		if forward.Port == port && forward.Status == "active" {
			return forward, true
		}
	}
	return state.ForwardState{}, false
}

// listeningPort holds a local listener so forward verification succeeds.
func listeningPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	return listener.Addr().(*net.TCPAddr).Port
}

func muxCalls(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(data)
}

func TestManagerWatchdogStallRestartsStreamAndKeepsForwards(t *testing.T) {
	r := newWatchdogRun(t)
	inventory := &runningInventory{ports: map[string][]int{discoveryA: {r.port}}}
	r.m.discovery.list, r.m.discovery.observe = inventory.list, inventory.observe
	r.start()
	stream := r.awaitStream(t)
	require.Eventually(t, func() bool { _, ok := activeForward(r.m, discoveryA, r.port); return ok }, 3*time.Second, time.Millisecond)
	before, _ := activeForward(r.m, discoveryA, r.port)
	calls := muxCalls(t, r.calls)

	r.stallStream(t, stream)

	select {
	case <-r.done:
		t.Fatalf("RUN_MUST_SURVIVE_STREAM_RESTART: %v", r.err)
	default:
	}
	after, ok := activeForward(r.m, discoveryA, r.port)
	require.True(t, ok, "FORWARDS_MUST_SURVIVE_STREAM_RESTART")
	require.Equal(t, before.CreatedAt, after.CreatedAt, "forward must not be rebuilt")
	require.Equal(t, calls, muxCalls(t, r.calls), "no forward is closed or reopened")
	require.Empty(t, r.m.history.GetAll(), "no forward ended")
	_, fatal := r.m.watchdog.terminalResult()
	require.False(t, fatal)
}

func TestManagerWatchdogRestartResyncsChangedContainers(t *testing.T) {
	r := newWatchdogRun(t)
	kept, stopped, started := r.port, listeningPort(t), listeningPort(t)
	inventory := &runningInventory{ports: map[string][]int{discoveryA: {kept}, discoveryB: {stopped}}}
	r.m.discovery.list, r.m.discovery.observe = inventory.list, inventory.observe
	// Freeze the discovery clock so periodic audits never become due: only the
	// stream restart may trigger the inventory pass that this test observes.
	frozen := time.Now()
	r.m.discovery.now = func() time.Time { return frozen }
	r.start()
	stream := r.awaitStream(t)
	require.Eventually(t, func() bool {
		_, a := activeForward(r.m, discoveryA, kept)
		_, b := activeForward(r.m, discoveryB, stopped)
		return a && b
	}, 3*time.Second, time.Millisecond)
	before, _ := activeForward(r.m, discoveryA, kept)

	// Containers change while the stream is silent: B stops and C starts.
	inventory.set(map[string][]int{discoveryA: {kept}, discoveryC: {started}})
	time.Sleep(200 * time.Millisecond)
	_, stillB := activeForward(r.m, discoveryB, stopped)
	require.True(t, stillB, "no audit may run before the restart")

	r.stallStream(t, stream)

	require.Eventually(t, func() bool {
		_, b := activeForward(r.m, discoveryB, stopped)
		_, c := activeForward(r.m, discoveryC, started)
		return !b && c
	}, 3*time.Second, time.Millisecond, "RESTART_MUST_RESYNC_FORWARDS")
	after, ok := activeForward(r.m, discoveryA, kept)
	require.True(t, ok)
	require.Equal(t, before.CreatedAt, after.CreatedAt, "unchanged forward must not be rebuilt")
	require.NotContains(t, muxCalls(t, r.calls), "CLOSE "+strconv.Itoa(kept)+" ", "unchanged forward must stay open")
	require.Contains(t, muxCalls(t, r.calls), "CLOSE "+strconv.Itoa(stopped)+" ")
	require.Contains(t, muxCalls(t, r.calls), "OPEN "+strconv.Itoa(started)+" ")
}

func TestManagerWatchdogEscalatesAfterFailedRestarts(t *testing.T) {
	r := newWatchdogRun(t)
	var logsMu sync.Mutex
	var restarts []string
	watchRunLogs(r.m, func(record slog.Record) {
		if strings.Contains(record.Message, "restarting") && strings.Contains(record.Message, "watchdog") {
			logsMu.Lock()
			restarts = append(restarts, record.Message)
			logsMu.Unlock()
		}
	})
	r.start()
	stream := r.awaitStream(t)
	for range 3 {
		stream = r.stallStream(t, stream)
	}
	logsMu.Lock()
	require.Len(t, restarts, 3, "each restart is logged")
	logsMu.Unlock()

	r.clock.Advance(60 * time.Second)
	r.tick(t)
	if !awaitWatchdog(t, stream.Done(), "WATCHDOG_MUST_ESCALATE_AFTER_FAILED_RESTARTS") {
		return
	}
	if !awaitWatchdog(t, r.done, "RUN_MUST_RETAIN_OWNED_JOIN") {
		return
	}
	require.ErrorContains(t, r.err, "no successfully handled Docker events")
	require.ErrorContains(t, r.err, "3 event stream restarts")
	require.True(t, r.m.stopping.Load())
	_, fatal := r.m.watchdog.terminalResult()
	require.True(t, fatal)
}
