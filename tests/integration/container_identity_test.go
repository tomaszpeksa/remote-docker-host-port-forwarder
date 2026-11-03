//go:build rdhpf_integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// No target lifecycle stimulus is retried. A separate no-port marker establishes
// actual event consumption before the single measured stop and same-ID restart.
func TestManagerPreexistingContainerIdentity(t *testing.T) {
	f := sshfixture.New(t)
	require.False(t, testing.Short(), "selected owned identity proof must execute fully")
	f.Configure("yes", f.PublicKey, "", f.Key, "no")
	t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "")
	remoteDocker := func(args ...string) string {
		return f.Docker(append([]string{"exec", f.Container, "docker"}, args...)...)
	}
	t.Logf("host Docker create client/server:\n%s", f.Docker("version"))
	t.Logf("application Docker client/server:\n%s", remoteDocker("version"))
	t.Logf("owned server packages:\n%s", f.Docker("exec", f.Container, "apk", "info", "-v"))
	require.Equal(t, "29.9.2", remoteDocker("version", "--format", "{{.Client.Version}}"))
	require.Empty(t, os.Getenv("DOCKER_API_VERSION"), "preserve API negotiation")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	id := f.Backend(sshfixture.Nginx, port)
	body := "identity-" + f.Alias
	f.Docker("exec", id, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
	marker := f.Backend(sshfixture.Alpine, 0)
	verifyOwned := func(containerID string) {
		require.Equal(t, containerID, remoteDocker("inspect", "--format", "{{.Id}}", containerID))
		require.Equal(t, f.Alias, remoteDocker("inspect", "--format", `{{index .Config.Labels "rdhpf.trust"}}`, containerID))
	}
	verifyOwned(marker)
	f.Docker("stop", "--time", "0", marker)
	verifyOwned(id)
	shortID := remoteDocker("ps", "--filter", "id="+id, "--format", "{{.ID}}")
	inventoryID := remoteDocker("ps", "--no-trunc", "--filter", "id="+id, "--format", "{{.ID}}")
	require.Equal(t, id[:12], shortID)
	require.Equal(t, id, inventoryID)
	t.Logf("identity before Manager: create=%s inspect=%s inventory=%s ordinary-ps=%s marker=%s", id, remoteDocker("inspect", "--format", "{{.Id}}", id), inventoryID, shortID, marker)
	t.Logf("backend image=%s pinned=%s", remoteDocker("inspect", "--format", "{{.Image}}", id), sshfixture.Nginx)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	ready, finishReady := context.WithTimeout(ctx, 5*time.Second)
	err = waitForNginxBackend(ready, f.Host, id)
	finishReady()
	require.NoError(t, err)
	owner := admitPublisher(t, f.Host)
	observer := identityStreamObserver{Handler: slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}), starts: &atomic.Int64{}, ends: &atomic.Int64{}, interrupted: &atomic.Bool{}}
	logger := slog.New(observer)
	master, err := newOwnedMaster(t, f.Host, logger)
	require.NoError(t, err)
	require.NoError(t, master.Open(ctx))
	st, history := state.NewState(), state.NewHistory()
	mgr := manager.NewManager(&config.Config{Host: f.Host}, docker.NewEventReader(f.Host, master.ControlPath(), logger), reconcile.NewReconciler(st, history, logger), master, st, history, logger)
	managerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	var runErr error
	go func() { runErr = mgr.Run(managerCtx, owner); close(done) }()
	// Join Manager, then close the owned mux before waiting for its event
	// command's pipes: a live mux can retain those descriptors after cancellation.
	// The helper's idempotent Close remains a fallback for startup failures.
	// Publisher/backend/config cleanups were registered earlier and run last.
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
			require.NoError(t, runErr)
		case <-time.After(15 * time.Second):
			t.Fatal("owned identity Manager did not join")
		}
		require.NoError(t, master.Close())
		if observer.starts.Load() > 0 {
			require.Eventually(t, func() bool { return observer.ends.Load() == observer.starts.Load() }, 5*time.Second, 20*time.Millisecond, "owned event command did not finish Wait")
		}
		t.Log("identity Manager/Master/event command joined before publisher/backend cleanup")
	})
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	defer transport.CloseIdleConnections()
	checkStream := func() {
		require.False(t, observer.interrupted.Load(), "identity proof cannot repair a stream interruption")
		require.LessOrEqual(t, observer.starts.Load(), int64(1), "identity proof cannot use a replacement stream")
	}
	await := func(description string, condition func() error) {
		bounded, finish := context.WithTimeout(ctx, 10*time.Second)
		defer finish()
		recoveryAwait(t, bounded, done, description, func() error {
			checkStream()
			return condition()
		})
	}
	assertOwner := func() {
		t.Helper()
		var desired []state.ContainerPorts
		for _, entry := range st.GetDesired() {
			require.NotEqual(t, shortID, entry.ContainerID, "IDENTITY_FULL_STARTUP_OWNER: no abbreviated owner or tombstone")
			if len(entry.Ports) > 0 {
				desired = append(desired, entry)
			}
		}
		require.Equal(t, []state.ContainerPorts{{ContainerID: id, Ports: []int{port}}}, desired, "IDENTITY_FULL_STARTUP_OWNER")
		actual := st.GetActual()
		require.Len(t, actual, 1)
		require.Equal(t, id, actual[0].ContainerID)
		require.Equal(t, port, actual[0].Port)
		require.Equal(t, "active", actual[0].Status)
	}
	await("startup owner and unique HTTP body", func() error {
		if len(st.GetActual()) != 1 || st.GetActual()[0].Status != "active" {
			return fmt.Errorf("startup actual=%v", st.GetActual())
		}
		return recoveryHTTP(client, port, body)
	})
	assertOwner()
	require.Empty(t, history.GetAll())
	t.Logf("IDENTITY_STARTUP full-owner=%s port=%d unique-HTTP200=%q", id, port, body)
	markerObserved := func() bool {
		for _, entry := range st.GetDesired() {
			if entry.ContainerID == marker {
				require.Empty(t, entry.Ports)
				return true
			}
		}
		return false
	}
	require.False(t, markerObserved(), "stopped marker must be absent from startup inventory")
	// Only setup-marker transitions may repeat. Polling observes state; neither
	// target transition below is retried or followed by manual reconciliation.
	handshakeEnd := time.Now().Add(10 * time.Second)
	for !markerObserved() {
		require.True(t, time.Now().Before(handshakeEnd), "no marker event consumed before handshake deadline")
		checkStream()
		verifyOwned(marker)
		f.Docker("start", marker)
		f.Docker("stop", "--time", "0", marker)
	}
	require.Empty(t, st.GetByContainer(marker))
	require.Equal(t, int64(1), observer.starts.Load())
	checkStream()
	t.Logf("IDENTITY_SUBSCRIBED marker=%s consumed by actual Manager", marker)
	since := time.Now().UTC().Format(time.RFC3339Nano)
	verifyOwned(id)
	f.Docker("stop", "--time", "5", id) // exactly one target stop
	await("stopped owner, history and TCP listener removal", func() error {
		if len(st.GetActual()) != 0 || len(history.GetAll()) != 1 {
			return fmt.Errorf("stop actual=%v history=%v", st.GetActual(), history.GetAll())
		}
		connection, dialErr := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if dialErr == nil {
			if err := connection.Close(); err != nil {
				return err
			}
			return fmt.Errorf("stale listener still accepts on %d", port)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return fmt.Errorf("listener absence requires refusal, not %w", dialErr)
		}
		return nil
	})
	require.Contains(t, st.GetDesired(), state.ContainerPorts{ContainerID: id, Ports: []int{}})
	for _, entry := range st.GetDesired() {
		require.Empty(t, entry.Ports)
		require.NotEqual(t, shortID, entry.ContainerID)
	}
	require.Equal(t, id, history.GetAll()[0].ContainerID)
	require.Equal(t, "container stopped", history.GetAll()[0].EndReason)
	t.Logf("IDENTITY_STOP once full-ID=%s owners=0 listener=ECONNREFUSED stop-history=1", id)
	verifyOwned(id)
	f.Docker("start", id) // exactly one target restart, never a new container
	require.Equal(t, id, remoteDocker("inspect", "--format", "{{.Id}}", id))
	await("same-ID restored owner and HTTP body", func() error {
		if len(st.GetActual()) != 1 || st.GetActual()[0].Status != "active" {
			return fmt.Errorf("restart actual=%v", st.GetActual())
		}
		return recoveryHTTP(client, port, body)
	})
	for observation := 0; observation < 10; observation++ {
		checkStream()
		assertOwner()
		require.Len(t, history.GetAll(), 1, "stop/die must not duplicate removal history")
		require.NoError(t, recoveryHTTP(client, port, body))
		select {
		case <-done:
			t.Fatal("Manager exited during identity quiescence")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	raw := remoteDocker("events", "--since", since, "--until", fmt.Sprint(time.Now().Unix()+1), "--filter", "container="+id, "--format", "{{json .}}")
	t.Logf("IDENTITY_NATIVE_EVENTS application CLI:\n%s", raw)
	counts := map[string]int{}
	for _, line := range strings.Split(raw, "\n") {
		var event struct {
			Action string
			Actor  struct{ ID string }
		}
		require.NoError(t, json.Unmarshal([]byte(line), &event))
		require.Equal(t, id, event.Actor.ID)
		counts[event.Action]++
	}
	require.Equal(t, 1, counts["stop"])
	require.Equal(t, 1, counts["die"])
	require.Equal(t, 1, counts["start"])
	checkStream()
	assertOwner()
	require.NoError(t, ctx.Err())
	f.NoPrompt()
	t.Logf("IDENTITY_RESTART_PROVED same-full-ID=%s owners=1 unique-HTTP200=%q target-stops=1 target-starts=1", id, body)
}

// Logs are used only to reject interruption and observe Wait completion, never
// as a subscription-ready signal. Readiness comes from the marker's State entry.
type identityStreamObserver struct {
	slog.Handler
	starts, ends *atomic.Int64
	interrupted  *atomic.Bool
}

func (o identityStreamObserver) Handle(ctx context.Context, record slog.Record) error {
	switch record.Message {
	case "docker events stream started":
		o.starts.Add(1)
	case "docker events stream ended":
		o.ends.Add(1)
	case "event stream restarted", "event stream error", "event channel closed", "event error channel closed", "DIAGNOSTIC: Docker events stream ended":
		o.interrupted.Store(true)
	}
	return o.Handler.Handle(ctx, record)
}
func (o identityStreamObserver) WithAttrs(attrs []slog.Attr) slog.Handler {
	o.Handler = o.Handler.WithAttrs(attrs)
	return o
}
func (o identityStreamObserver) WithGroup(name string) slog.Handler {
	o.Handler = o.Handler.WithGroup(name)
	return o
}
