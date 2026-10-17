//go:build rdhpf_integration

package manager

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
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

func TestWatchdogSSHProxy(t *testing.T) {
	if os.Getenv("RDHPF_WATCHDOG_SOCKET") != "" {
		os.Exit(sshfixture.WatchdogProxyMain())
	}
}

type watchdogOwned struct {
	t                  *testing.T
	f                  *sshfixture.WatchdogFixture
	m                  *Manager
	owner              *instancepath.Owner
	ctx                context.Context
	cancel             context.CancelFunc
	done               chan struct{}
	err                error
	stream             chan context.Context
	epoch              chan time.Time
	tickEpoch          time.Time
	probeContexts      chan context.Context
	closeOnce          sync.Once
	requirePingCreated bool
	pingCount          int
}

func newWatchdogOwned(t *testing.T) *watchdogOwned {
	t.Helper()
	f := sshfixture.NewWatchdog(t)
	require.False(t, testing.Short())
	require.Empty(t, os.Getenv("DOCKER_API_VERSION"))
	t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "")
	require.Equal(t, f.Docker("info", "--format", "{{.ID}}"), f.Docker("exec", f.Container, "docker", "info", "--format", "{{.ID}}"), "same owned Engine")
	t.Logf("WATCHDOG_ENGINE %s", f.Docker("exec", f.Container, "docker", "version"))
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	master, err := ssh.NewMaster(f.Host, logger)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); require.NoError(t, master.Close()) })
	require.NoError(t, master.Open(ctx))
	st, history := state.NewState(), state.NewHistory()
	h := &watchdogOwned{t: t, f: f, ctx: ctx, cancel: cancel, stream: make(chan context.Context, 4), epoch: make(chan time.Time, 1), probeContexts: make(chan context.Context, 16)}
	h.m = NewManager(&config.Config{Host: f.Host}, docker.NewEventReader(f.Host, master.ControlPath(), logger), reconcile.NewReconciler(st, history, logger), master, st, history, logger)
	h.owner, err = instancepath.Acquire(f.Host)
	require.NoError(t, err)
	raw := h.m.discovery.openStream
	h.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		h.stream <- ctx
		return raw(ctx, deliver)
	}
	h.m.watchdog.ticks = func() (<-chan time.Time, func()) {
		epoch := time.Now()
		h.tickEpoch = epoch
		ticker := time.NewTicker(10 * time.Second)
		h.epoch <- epoch
		return ticker.C, ticker.Stop
	}
	ping := h.m.watchdog.dockerPing
	h.m.watchdog.dockerPing = watchdogPingFunc(func(ctx context.Context) error { h.probeContexts <- ctx; return ping.Ping(ctx) })
	t.Cleanup(h.close)
	return h
}
func (h *watchdogOwned) start() context.Context {
	h.done = make(chan struct{})
	go func() { h.err = h.m.Run(h.ctx, h.owner); close(h.done) }()
	select {
	case ctx := <-h.stream:
		return ctx
	case <-h.done:
		h.t.Fatalf("Run exited before stream: %v", h.err)
	case <-time.After(10 * time.Second):
		h.t.Fatal("owned real stream did not open")
	}
	return nil
}
func (h *watchdogOwned) close() {
	h.closeOnce.Do(func() {
		joined := false
		defer func() { h.pingCount = h.f.SchedulePingAccounting(h.requirePingCreated, joined) }()
		h.cancel()
		h.f.Close()
		if h.done != nil {
			select {
			case <-h.done:
			case <-time.After(15 * time.Second):
				h.t.Fatal("WATCHDOG_MANAGER_JOIN_PENDING")
			}
		}
		require.NoError(h.t, h.m.sshMaster.Close())
		require.NoError(h.t, h.owner.Close())
		joined = true
		h.t.Logf("WATCHDOG_RUN_JOINED result=%v", h.err)
		h.f.NoPrompt()
	})
}
func (h *watchdogOwned) wait(duration time.Duration, marker string, predicate func() bool) {
	h.t.Helper()
	require.Eventually(h.t, predicate, duration, 10*time.Millisecond, marker)
}
func (h *watchdogOwned) backend() (string, int, string) {
	h.t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(h.t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(h.t, listener.Close())
	return h.backendAtPort(port)
}
func (h *watchdogOwned) backendAtPort(port int) (string, int, string) {
	h.t.Helper()
	id := h.f.Backend(sshfixture.Nginx, port)
	h.t.Cleanup(h.close)
	body := fmt.Sprintf("watchdog-%s-%d", h.f.Alias, port)
	h.f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
	return id, port, body
}
func watchdogHTTP(port int, body string) bool {
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return false
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	return err == nil && response.StatusCode == 200 && string(data) == body
}
func (h *watchdogOwned) fatal(ctx context.Context, last time.Time) {
	h.t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(75 * time.Second):
		h.t.Fatal("WATCHDOG_REAL_ACTION_MISSING")
	}
	h.m.watchdog.mu.RLock()
	selected, err, fatal := h.m.watchdog.selectedAt, h.m.watchdog.result, h.m.watchdog.watchdogFailure
	h.m.watchdog.mu.RUnlock()
	require.True(h.t, fatal)
	require.ErrorContains(h.t, err, "no successfully handled Docker events")
	require.NoError(h.t, h.ctx.Err(), "external cancel is not proof")
	require.GreaterOrEqual(h.t, selected.Sub(last), 60*time.Second)
	require.Less(h.t, selected.Sub(last), 71*time.Second)
	check := h.tickEpoch.Add(10 * time.Second)
	for check.Before(last.Add(60 * time.Second)) {
		check = check.Add(10 * time.Second)
	}
	require.False(h.t, selected.Before(check))
	require.Less(h.t, selected.Sub(check), time.Second, "FIRST_ELIGIBLE_CHECK_SCHEDULING_PROOF_BLOCKED")
	h.t.Logf("WATCHDOG_REAL_ACTION last_success=%s selected=%s observed_cancel=%s silence=%s", last.Format(time.RFC3339Nano), selected.Format(time.RFC3339Nano), time.Now().Format(time.RFC3339Nano), selected.Sub(last))
}

func TestWatchdogOwnedWarmProcessing(t *testing.T) {
	h := newWatchdogOwned(t)
	h.start()
	arm := lastWatchdogHealth(h.m.watchdog)
	id, port, body := h.backend()
	h.wait(15*time.Second, "owned forwarded HTTP", func() bool { return watchdogHTTP(port, body) })
	h.f.FailAfterCompletedPing()
	if os.Getenv("RDHPF_WATCHDOG_FAILURE_CONTROL") == "backend" {
		h.f.ObserveCompletedPingForFailure()
		name := fmt.Sprintf("%s-%d", h.f.Alias, port)
		require.Equal(t, id+" /"+name+" "+h.f.Alias, h.f.Docker("inspect", "--format", `{{.Id}} {{.Name}} {{index .Config.Labels "rdhpf.trust"}}`, id))
		t.Logf("WATCHDOG_INTENTIONAL_BACKEND_COLLISION name=%s Engine_ID=%s", name, id)
		h.backendAtPort(port) // Fails inside Backend, after its removal registration.
		t.Fatal("owned duplicate Backend unexpectedly returned")
	}
	timer := time.NewTimer(time.Until(arm.Add(130 * time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-h.done:
		t.Fatalf("healthy Manager exited: %v", h.err)
	}
	require.Greater(t, lastWatchdogHealth(h.m.watchdog).Sub(arm), 90*time.Second, "real successful ping processing over multiple horizons")
	require.True(t, watchdogHTTP(port, body))
	require.NoError(t, h.ctx.Err())
	h.requirePingCreated = true
	h.close()
	require.NoError(t, h.err)
	require.GreaterOrEqual(t, h.pingCount, 3)
	t.Log("WATCHDOG_WARM_130_SECONDS actual Run/SSH/Engine/HTTP remained healthy")
}

func TestWatchdogOwnedColdPull(t *testing.T) {
	h := newWatchdogOwned(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", h.f.Container, "docker", "image", "inspect", "alpine:latest")
	output, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err())
	require.Error(t, err, "COLD_PROOF_BLOCKED: alpine:latest was already cached; never delete it")
	require.Contains(t, string(output), "No such image: alpine:latest", "generic infrastructure error is not a cache miss")
	t.Logf("WATCHDOG_COLD_VERIFIED_MISS %s", output)
	h.start()
	arm := lastWatchdogHealth(h.m.watchdog)
	h.wait(75*time.Second, "cold implicit pull must process or select fatal", func() bool { return lastWatchdogHealth(h.m.watchdog).After(arm) || h.m.stopping.Load() })
	h.m.watchdog.mu.RLock()
	cause := h.m.watchdog.result
	h.m.watchdog.mu.RUnlock()
	t.Logf("WATCHDOG_COLD_OUTCOME duration=%s successful_processing=%v fatal=%v", time.Since(arm), lastWatchdogHealth(h.m.watchdog).After(arm), cause)
	h.requirePingCreated = cause == nil
	h.close()
	if cause == nil {
		require.NoError(t, h.err)
		t.Logf("WATCHDOG_COLD_IMAGE_DIGEST %s", h.f.Docker("image", "inspect", "alpine:latest", "--format", "{{json .RepoDigests}}"))
	} else {
		require.Same(t, cause, h.err)
	}
}

func TestWatchdogOwnedBlockedWork(t *testing.T) {
	t.Run("Probe", func(t *testing.T) { watchdogOwnedHeldProbe(t, false) })
	t.Run("Inspect", func(t *testing.T) {
		h := newWatchdogOwned(t)
		dispatch, releaseDispatch := h.f.GatePingDispatch()
		defer releaseDispatch()
		handler := make(chan time.Time, 4)
		watchRunLogs(h.m, func(record slog.Record) {
			if record.Message == "handling container start" {
				handler <- record.Time
			}
		})
		ctx := h.start()
		epoch := <-h.epoch
		select {
		case <-dispatch:
		case <-time.After(45 * time.Second):
			t.Fatal("probe dispatch hold not entered")
		}
		last := lastWatchdogHealth(h.m.watchdog)
		check := epoch.Add(10 * time.Second)
		for check.Before(last.Add(60 * time.Second)) {
			check = check.Add(10 * time.Second)
		}
		entered, release := h.f.Gate("inspect")
		defer release()
		timer := time.NewTimer(time.Until(check.Add(-2500 * time.Millisecond)))
		defer timer.Stop()
		<-timer.C
		id, _, _ := h.backend()
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("INSPECT_SCHEDULING_PROOF_BLOCKED: missed real inspection entry")
		}
		began := <-handler
		require.True(t, began.Before(check), "INSPECT_SCHEDULING_PROOF_BLOCKED")
		h.fatal(ctx, last)
		h.wait(time.Second, "actual inspection proxy cancellation before own deadline", func() bool {
			for _, a := range h.f.Snapshot() {
				if a.Operation == "inspect" && a.ContainerID == id && !a.CanceledAt.IsZero() {
					return true
				}
			}
			return false
		})
		matched := false
		for _, a := range h.f.Snapshot() {
			if a.Operation == "inspect" && a.ContainerID == id {
				matched = true
				require.True(t, a.RealChildStarted)
				require.True(t, a.CanceledAt.Before(began.Add(5*time.Second)), "unchanged inspection timeout cannot be oracle")
				require.False(t, a.JoinedAt.IsZero())
				require.True(t, a.CompletedAt.IsZero())
				t.Logf("WATCHDOG_INSPECT_BOUNDARY epoch=%s first_check=%s handler_entered=%s own_deadline_not_before=%s record=%+v", epoch, check, began, began.Add(5*time.Second), a)
			}
		}
		require.True(t, matched)
		release()
		releaseDispatch()
		h.close()
		require.ErrorContains(t, h.err, "no successfully handled Docker events")
	})
}
func watchdogOwnedHeldProbe(t *testing.T, allowance bool) {
	h := newWatchdogOwned(t)
	events, releaseEvents := h.f.Gate("events")
	defer releaseEvents()
	probe, releaseProbe := h.f.Gate("ping")
	defer releaseProbe()
	ctx := h.start()
	last := lastWatchdogHealth(h.m.watchdog)
	select {
	case <-probe:
	case <-time.After(45 * time.Second):
		t.Fatal("actual ping output did not reach held boundary")
	}
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("real ping events missing")
	}
	attemptCtx := <-h.probeContexts
	deadline, ok := attemptCtx.Deadline()
	require.True(t, ok)
	require.Equal(t, last.Add(60*time.Second), deadline)
	h.fatal(ctx, last)
	h.wait(time.Second, "retained real ping child joined after cancellation", func() bool {
		for _, a := range h.f.Snapshot() {
			if a.Operation == "ping" && a.RealChildStarted && !a.JoinedAt.IsZero() && !a.CanceledAt.IsZero() {
				return true
			}
		}
		return false
	})
	for _, a := range h.f.Snapshot() {
		if a.Operation == "ping" {
			require.True(t, a.CompletedAt.IsZero())
			require.False(t, a.DispatchSuppressed)
			t.Logf("WATCHDOG_PROBE_REAL_SSH %+v", a)
		}
	}
	if allowance {
		require.True(t, time.Now().After(deadline))
		t.Log("WATCHDOG_PULL_ALLOWANCE controlled completion delay beyond remaining allowance; not a cold download")
	}
	releaseProbe()
	releaseEvents()
	h.requirePingCreated = true
	h.close()
	require.ErrorContains(t, h.err, "no successfully handled Docker events")
	require.Equal(t, 1, h.pingCount)
}
func TestWatchdogOwnedPullAllowance(t *testing.T) { watchdogOwnedHeldProbe(t, true) }
