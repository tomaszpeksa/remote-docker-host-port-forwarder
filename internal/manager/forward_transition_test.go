package manager

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func transitionProbePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	return listener.Addr().(*net.TCPAddr).Port
}

func TestManagerRetiredUnavailableRestoresSameGeneration(t *testing.T) {
	port := transitionProbePort(t)
	r := newWatchdogRun(t)
	r.m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryA}, nil }
	r.m.discovery.observe = func(context.Context, string) ([]int, error) { return []int{port}, nil }
	r.start()
	r.awaitStream(t)
	require.Eventually(t, func() bool {
		actual := r.m.state.GetByContainer(discoveryA)
		return len(actual) == 1 && actual[0].Status == "active" && !r.m.hasUnapplied()
	}, 3*time.Second, 10*time.Millisecond)
	created := r.m.state.GetByContainer(discoveryA)[0].CreatedAt

	// Retire the actual retained child, then fail replacement before Start.
	// Open begins the successor lifecycle, so its gate exposes retirement even
	// while unavailable. No synthetic generation or manual Apply is used.
	require.NoError(t, r.m.sshMaster.Close())
	launcher := filepath.Join(os.Getenv("PATH"), "ssh")
	require.NoError(t, os.Rename(launcher, launcher+"-held"))
	held := true
	defer func() {
		if held {
			require.NoError(t, os.Rename(launcher+"-held", launcher))
		}
	}()
	require.ErrorContains(t, r.m.sshMaster.Open(context.Background()), "failed to start SSH ControlMaster")
	require.Eventually(t, func() bool { return len(r.m.state.GetResources()) == 0 }, time.Second, time.Millisecond,
		"retired authority must be invalidated while replacement is unavailable")
	var retired uint64
	require.NoError(t, r.m.sshMaster.WithConnection(context.Background(), func(generation uint64, ready bool) error {
		retired = generation
		require.False(t, ready)
		return nil
	}))
	require.Positive(t, retired)
	require.Equal(t, created, r.m.state.GetByContainer(discoveryA)[0].CreatedAt)
	require.Empty(t, r.m.history.GetAll())
	require.NoError(t, os.Rename(launcher+"-held", launcher))
	held = false
	require.NoError(t, r.m.sshMaster.Open(context.Background()))
	require.NoError(t, r.m.sshMaster.WithConnection(context.Background(), func(generation uint64, ready bool) error {
		require.Equal(t, retired, generation)
		require.True(t, ready)
		return nil
	}))
	// No new desired event, changed inventory, or manual reconciliation.
	require.Eventually(t, func() bool {
		actual := r.m.state.GetByContainer(discoveryA)
		return len(actual) == 1 && actual[0].Status == "active"
	}, 2500*time.Millisecond, 10*time.Millisecond, "RETIRED_UNAVAILABLE_MUST_RESTORE_SAME_GENERATION")
	require.Equal(t, created, r.m.state.GetByContainer(discoveryA)[0].CreatedAt)
	require.Empty(t, r.m.history.GetAll(), "recovery must not consume terminal history")
}

func TestManagerRestartDuringCloseRestoresWithoutEvent(t *testing.T) {
	port, sibling := transitionProbePort(t), transitionProbePort(t)
	r := newWatchdogRun(t)
	r.m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryA, discoveryB}, nil }
	r.m.discovery.observe = func(_ context.Context, id string) ([]int, error) {
		if id == discoveryA {
			return []int{port}, nil
		}
		return []int{sibling}, nil
	}
	r.start()
	r.awaitStream(t)
	require.Eventually(t, func() bool {
		return len(r.m.state.GetResources()) == 2 && !r.m.hasUnapplied()
	}, 3*time.Second, 10*time.Millisecond)
	created := r.m.state.GetByContainer(discoveryA)[0].CreatedAt
	fail := os.Getenv("S12_MUX_CLOSE_FAIL")
	require.NoError(t, os.WriteFile(fail, []byte("injected CLOSE rejection"), 0600))
	require.NoError(t, r.m.setEventDesired(context.Background(), 0, discoveryA, nil))
	r.m.wakeReconciliation()
	require.Eventually(t, func() bool {
		resource, ok := r.m.state.Resource(discoveryA, port)
		return ok && resource.Remove && !resource.RetryAt.IsZero() && !r.m.hasUnapplied()
	}, time.Second, time.Millisecond)
	require.NoError(t, r.m.setEventDesired(context.Background(), 0, discoveryA, []int{port}))
	r.m.wakeReconciliation()
	require.Eventually(t, func() bool { return !r.m.hasUnapplied() }, time.Second, time.Millisecond)
	resource, ok := r.m.state.Resource(discoveryA, port)
	require.True(t, ok)
	require.True(t, resource.Remove)
	require.Equal(t, state.ForwardOwned, resource.Ownership)
	require.Empty(t, r.m.history.GetAll())
	calls, err := os.ReadFile(os.Getenv("S12_MUX_RECORD"))
	require.NoError(t, err)
	open := fmt.Sprintf("OPEN %d %d\n", port, port)
	require.Equal(t, 1, strings.Count(string(calls), open), "no reopening while old CLOSE is owed")
	require.NoError(t, os.Remove(fail))
	// Only the existing resource timer can now complete CLOSE and restore desire.
	require.Eventually(t, func() bool { return len(r.m.history.GetAll()) == 1 }, 2*time.Second, 10*time.Millisecond)
	closedAt := time.Now()
	require.Eventually(t, func() bool {
		actual := r.m.state.GetByContainer(discoveryA)
		return len(actual) == 1 && actual[0].Status == "active"
	}, 2500*time.Millisecond, 10*time.Millisecond, "RESTART_DURING_CLOSE_MUST_RESTORE_WITHOUT_EVENT")
	require.GreaterOrEqual(t, time.Since(closedAt), 900*time.Millisecond, "renewed OPEN must retain resource pacing")
	history := r.m.history.GetAll()
	require.Len(t, history, 1)
	require.Equal(t, created, history[0].StartedAt)
	require.Equal(t, "container stopped", history[0].EndReason)
	require.Equal(t, "active", r.m.state.GetByContainer(discoveryB)[0].Status)
	calls, err = os.ReadFile(os.Getenv("S12_MUX_RECORD"))
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(calls), open))
	require.Equal(t, 1, strings.Count(string(calls), fmt.Sprintf("OPEN %d %d\n", sibling, sibling)))
	require.Less(t, strings.LastIndex(string(calls), fmt.Sprintf("CLOSE %d %d\n", port, port)), strings.LastIndex(string(calls), open))
}
