//go:build rdhpf_integration

package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

type discoveryBackend struct {
	id, body string
	port     int
}
type discoveryOwned struct {
	t         *testing.T
	f         *sshfixture.Fixture
	m         *Manager
	owner     *instancepath.Owner
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	runErr    error
	joined    sync.Once
	events    chan docker.Event
	rawStream func(context.Context, func(context.Context, docker.Event) error) <-chan docker.StreamResult
	client    *http.Client
}

func newDiscoveryOwned(t *testing.T) *discoveryOwned {
	t.Helper()
	return newDiscoveryOwnedFixture(t, sshfixture.New(t))
}

func newDiscoveryOwnedFixture(t *testing.T, f *sshfixture.Fixture) *discoveryOwned {
	t.Helper()
	require.False(t, testing.Short(), "selected discovery proof must execute fully")
	f.Configure("yes", f.PublicKey, "", f.Key, "no")
	t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "")
	require.Empty(t, os.Getenv("DOCKER_API_VERSION"))
	t.Logf("host create CLI/Engine/API:\n%s", f.Docker("version"))
	t.Logf("application CLI/Engine/API:\n%s", f.Docker("exec", f.Container, "docker", "version"))
	require.Equal(t, "29.9.2", f.Docker("exec", f.Container, "docker", "version", "--format", "{{.Client.Version}}"))
	packages := f.Docker("exec", f.Container, "apk", "info", "-v")
	require.Contains(t, strings.Fields(packages), "docker-cli-29.9.2-r0")
	t.Logf("owned server packages:\n%s", packages)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	master, err := ssh.NewMaster(f.Host, logger)
	require.NoError(t, err)
	h := &discoveryOwned{t: t, f: f, ctx: ctx, cancel: cancel, events: make(chan docker.Event, 64)}
	// Register before potentially failing setup; subsequently registered joins
	// precede every backend cleanup, including backends created after Run starts.
	t.Cleanup(func() { cancel(); require.NoError(t, master.Close()) })
	require.NoError(t, master.Open(ctx))
	st, history := state.NewState(), state.NewHistory()
	h.m = NewManager(&config.Config{Host: f.Host}, docker.NewEventReader(f.Host, master.ControlPath(), logger), reconcile.NewReconciler(st, history, logger), master, st, history, logger)
	h.owner, err = instancepath.Acquire(f.Host)
	require.NoError(t, err)
	h.rawStream = h.m.discovery.openStream
	h.m.discovery.openStream = h.openStream
	transport := &http.Transport{DisableKeepAlives: true}
	h.client = &http.Client{Transport: transport, Timeout: time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	t.Cleanup(h.close)
	return h
}

func (h *discoveryOwned) close() {
	h.joined.Do(func() {
		h.cancel()
		if h.done != nil {
			select {
			case <-h.done:
				if h.runErr != nil {
					h.t.Errorf("owned Manager returned error: %v", h.runErr)
				}
			case <-time.After(15 * time.Second):
				h.t.Fatal("discovery Manager did not join")
			}
		}
		require.NoError(h.t, h.m.sshMaster.Close())
		require.NoError(h.t, h.owner.Close())
		h.f.NoPrompt()
		h.t.Log("DISCOVERY_JOINED Manager/inventory/reconciliation/stream/Master before owner/backend cleanup")
	})
}
func (h *discoveryOwned) start() {
	h.done = make(chan struct{})
	go func() { h.runErr = h.m.Run(h.ctx, h.owner); close(h.done) }()
	h.t.Cleanup(h.close)
}
func (h *discoveryOwned) backend() discoveryBackend {
	h.t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(h.t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(h.t, listener.Close())
	id := h.f.Backend(sshfixture.Nginx, port)
	h.t.Cleanup(h.close)
	body := fmt.Sprintf("discovery-%s-%d", h.f.Alias, port)
	h.f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
	h.verify(id)
	h.t.Logf("DISCOVERY_BACKEND full-ID=%s port=%d unique-body=%s", id, port, body)
	return discoveryBackend{id: id, body: body, port: port}
}
func (h *discoveryOwned) verify(id string) {
	require.Equal(h.t, id, h.f.Docker("inspect", "--format", "{{.Id}}", id))
	require.Equal(h.t, h.f.Alias, h.f.Docker("inspect", "--format", `{{index .Config.Labels "rdhpf.trust"}}`, id))
}
func (h *discoveryOwned) stop(b discoveryBackend) {
	h.verify(b.id)
	h.f.Docker("stop", "--time", "0", b.id)
	h.t.Logf("DISCOVERY_TARGET_STOP_ONCE %s", b.id)
}
func (h *discoveryOwned) openStream(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
	return h.rawStream(ctx, func(ctx context.Context, event docker.Event) error {
		if err := deliver(ctx, event); err != nil {
			return err
		}
		select {
		case h.events <- event:
		default:
		}
		return nil
	})
}

// The gate is before command dispatch, not an invented subscription-ready signal.
func (h *discoveryOwned) gatedStream(ctx context.Context, deliver func(context.Context, docker.Event) error, held, release chan struct{}) <-chan docker.StreamResult {
	result := make(chan docker.StreamResult, 1)
	go func() {
		defer close(result)
		close(held)
		select {
		case <-ctx.Done():
			result <- docker.StreamResult{Err: ctx.Err(), Canceled: true}
		case <-release:
			result <- <-h.openStream(ctx, deliver)
		}
	}()
	return result
}
func (h *discoveryOwned) wait(marker string, condition func() error) {
	h.t.Helper()
	end := time.Now().Add(16 * time.Second)
	var last error
	for time.Now().Before(end) {
		require.NoError(h.t, h.ctx.Err())
		select {
		case <-h.done:
			h.t.Fatalf("Manager exited: %v", h.runErr)
		default:
		}
		last = condition()
		if last == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.NoError(h.t, last, marker)
}
func (h *discoveryOwned) active(b discoveryBackend) error {
	actual := h.m.state.GetByContainer(b.id)
	if len(actual) != 1 || actual[0].Port != b.port || actual[0].Status != "active" {
		return fmt.Errorf("owner %s actual=%v", b.id, actual)
	}
	response, err := h.client.Get(fmt.Sprintf("http://127.0.0.1:%d/", b.port))
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
	closeErr := response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if response.StatusCode != http.StatusOK || string(body) != b.body {
		return fmt.Errorf("unique HTTP200 mismatch: status=%d body=%q", response.StatusCode, body)
	}
	return nil
}
func (h *discoveryOwned) absent(b discoveryBackend) error {
	if len(h.m.state.GetByContainer(b.id)) != 0 {
		return fmt.Errorf("stale actual owner %s", b.id)
	}
	connection, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", b.port), 200*time.Millisecond)
	if err == nil {
		connection.Close()
		return fmt.Errorf("stale listener %d", b.port)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("expected ECONNREFUSED: %w", err)
	}
	return nil
}
func (h *discoveryOwned) owners(backends ...discoveryBackend) {
	expected := []state.ContainerPorts{}
	for _, b := range backends {
		expected = append(expected, state.ContainerPorts{ContainerID: b.id, Ports: []int{b.port}})
	}
	desired := []state.ContainerPorts{}
	for _, cp := range h.m.state.GetDesired() {
		require.Len(h.t, cp.ContainerID, 64)
		if len(cp.Ports) > 0 {
			desired = append(desired, cp)
		}
	}
	require.ElementsMatch(h.t, expected, desired, "DISCOVERY_FULL_OWNERS")
	require.Len(h.t, h.m.state.GetActual(), len(backends))
	for _, b := range backends {
		require.NoError(h.t, h.active(b))
	}
}
func (h *discoveryOwned) history(b discoveryBackend, created time.Time) {
	history := h.m.history.GetAll()
	require.Len(h.t, history, 1)
	require.Equal(h.t, b.id, history[0].ContainerID)
	require.Equal(h.t, "container stopped", history[0].EndReason)
	require.Equal(h.t, created, history[0].StartedAt)
}
func (h *discoveryOwned) subscribed() {
	marker := h.f.Backend(sshfixture.Alpine, 0)
	h.t.Cleanup(h.close)
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		for len(h.events) > 0 {
			if (<-h.events).ContainerID == marker {
				h.t.Logf("DISCOVERY_NATIVE_EVENT_ACCEPTED setup-marker=%s", marker)
				return
			}
		}
		// Only a no-port setup marker may repeat; measured targets never do.
		h.f.Docker("stop", "--time", "0", marker)
		h.f.Docker("start", marker)
	}
	h.t.Fatal("no actual setup-marker event accepted")
}

func TestManagerDiscoveryOwnedStartupGap(t *testing.T) {
	h := newDiscoveryOwned(t)
	a := h.backend()
	held, release := make(chan struct{}), make(chan struct{})
	h.m.discovery.openStream = func(ctx context.Context, d func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		return h.gatedStream(ctx, d, held, release)
	}
	h.start()
	<-held
	h.wait("DISCOVERY_INITIAL_A", func() error { return h.active(a) })
	created := h.m.state.GetByContainer(a.id)[0].CreatedAt
	h.t.Log("DISCOVERY_GAP initial inventory applied; stream command still not dispatched")
	h.stop(a)
	b := h.backend()
	h.wait("DISCOVERY_STARTUP_GAP_TIMER", func() error {
		if err := h.active(b); err != nil {
			return err
		}
		return h.absent(a)
	})
	h.owners(b)
	h.history(a, created)
	h.t.Log("DISCOVERY_STARTUP_GAP_REPAIRED without events or repeated target transition")
	close(release)
}

func TestManagerDiscoveryOwnedInventoryOverlap(t *testing.T) {
	h := newDiscoveryOwned(t)
	a := h.backend()
	var arm atomic.Bool
	var captured atomic.Bool
	var listExpired atomic.Bool
	listed, release, next := make(chan struct{}), make(chan struct{}), make(chan struct{})
	realList := h.m.discovery.list
	h.m.discovery.list = func(ctx context.Context) ([]string, error) {
		ids, err := realList(ctx)
		if err != nil {
			return nil, err
		}
		if arm.CompareAndSwap(true, false) {
			captured.Store(true)
			close(listed)
			select {
			case <-release:
			case <-ctx.Done():
				listExpired.Store(true)
				return nil, ctx.Err()
			}
		} else if captured.Load() {
			select {
			case <-next:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return ids, nil
	}
	completed := make(chan struct{})
	var once sync.Once
	var released atomic.Bool
	h.m.discovery.after = func(d time.Duration) <-chan time.Time {
		if released.Load() {
			once.Do(func() { close(completed) })
		}
		return time.After(d)
	}
	h.start()
	h.wait("DISCOVERY_INITIAL_A", func() error { return h.active(a) })
	created := h.m.state.GetByContainer(a.id)[0].CreatedAt
	h.subscribed()
	arm.Store(true)
	<-listed
	h.t.Log("DISCOVERY_OVERLAP real complete list captured before target stop/start")
	h.stop(a)
	b := h.backend()
	h.wait("DISCOVERY_ACCEPTED_EVENTS", func() error {
		desired := h.m.state.GetDesired()
		emptyA, fullB := false, false
		for _, cp := range desired {
			if cp.ContainerID == a.id && len(cp.Ports) == 0 {
				emptyA = true
			}
			if cp.ContainerID == b.id && len(cp.Ports) == 1 && cp.Ports[0] == b.port {
				fullB = true
			}
		}
		if !emptyA || !fullB {
			return fmt.Errorf("events not yet applied: %v", desired)
		}
		return nil
	})
	require.False(t, listExpired.Load(), "owned overlap gate exceeded list observation budget")
	released.Store(true)
	close(release)
	<-completed
	require.False(t, listExpired.Load(), "owned overlap gate exceeded list observation budget")
	require.Contains(t, h.m.state.GetDesired(), state.ContainerPorts{ContainerID: a.id, Ports: []int{}}, "DISCOVERY_STALE_FENCE")
	require.Contains(t, h.m.state.GetDesired(), state.ContainerPorts{ContainerID: b.id, Ports: []int{b.port}}, "DISCOVERY_STALE_FENCE")
	close(next)
	h.wait("DISCOVERY_OVERLAP_TRAFFIC", func() error {
		if err := h.active(b); err != nil {
			return err
		}
		return h.absent(a)
	})
	h.owners(b)
	h.history(a, created)
}

func TestManagerDiscoveryOwnedDowntime(t *testing.T) {
	h := newDiscoveryOwned(t)
	a := h.backend()
	var calls atomic.Int64
	cancelFirst := make(chan context.CancelFunc, 1)
	held, release := make(chan struct{}), make(chan struct{})
	h.m.discovery.openStream = func(ctx context.Context, d func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		if calls.Add(1) == 1 {
			child, cancel := context.WithCancel(ctx)
			cancelFirst <- cancel
			result := make(chan docker.StreamResult, 1)
			go func() { defer close(result); defer cancel(); result <- <-h.openStream(child, d) }()
			return result
		}
		return h.gatedStream(ctx, d, held, release)
	}
	h.start()
	h.wait("DISCOVERY_INITIAL_A", func() error { return h.active(a) })
	created := h.m.state.GetByContainer(a.id)[0].CreatedAt
	h.subscribed()
	(<-cancelFirst)()
	<-held
	h.t.Log("DISCOVERY_DOWNTIME previous native stream joined; replacement not dispatched")
	h.stop(a)
	b := h.backend()
	h.wait("DISCOVERY_DOWNTIME_TIMER", func() error {
		if err := h.active(b); err != nil {
			return err
		}
		return h.absent(a)
	})
	h.owners(b)
	h.history(a, created)
	require.Equal(t, int64(2), calls.Load())
	close(release)
}

func TestManagerDiscoveryOwnedInspectRetry(t *testing.T) {
	h := newDiscoveryOwned(t)
	a := h.backend()
	held, release := make(chan struct{}), make(chan struct{})
	h.m.discovery.openStream = func(ctx context.Context, d func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		return h.gatedStream(ctx, d, held, release)
	}
	var failedID atomic.Value
	failedID.Store("")
	var failures atomic.Int64
	realObserve := h.m.discovery.observe
	h.m.discovery.observe = func(ctx context.Context, id string) ([]int, error) {
		ports, err := realObserve(ctx, id)
		if err == nil && id == failedID.Load().(string) {
			failures.Add(1)
			return nil, fmt.Errorf("controlled unknown after real owned observation")
		}
		return ports, err
	}
	h.start()
	<-held
	h.wait("DISCOVERY_INITIAL_A", func() error { return h.active(a) })
	created := h.m.state.GetByContainer(a.id)[0].CreatedAt
	// The new B is unknown while independent C and complete-list absence progress.
	h.stop(a)
	b := h.backend()
	failedID.Store(b.id)
	c := h.backend()
	h.wait("DISCOVERY_PARTIAL_C", func() error {
		if err := h.active(c); err != nil {
			return err
		}
		return h.absent(a)
	})
	require.Positive(t, failures.Load())
	require.Empty(t, h.m.state.GetByContainer(b.id))
	for _, cp := range h.m.state.GetDesired() {
		require.NotEqual(t, b.id, cp.ContainerID, "unknown startup ID must not be invented")
	}
	h.t.Logf("DISCOVERY_PARTIAL_PROGRESS absent=%s unknown=%s successful=%s", a.id, b.id, c.id)
	failedID.Store("") // No target event, lifecycle action or explicit reconcile follows.
	h.wait("DISCOVERY_INSPECT_RETRY", func() error { return h.active(b) })
	h.owners(b, c)
	h.history(a, created)
	close(release)
}
