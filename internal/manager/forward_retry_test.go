package manager

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagerConflictReleasedWithoutEvent(t *testing.T) {
	r := newWatchdogRun(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	port := listener.Addr().(*net.TCPAddr).Port
	release := os.Getenv("S12_MUX_OPEN_FAIL")
	require.NoError(t, os.WriteFile(release, []byte("address already in use"), 0600))
	r.m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryA}, nil }
	r.m.discovery.observe = func(context.Context, string) ([]int, error) { return []int{port}, nil }
	// Open returns only after readiness and its synchronous startup notify.
	// Consume that notification before Run owns the reconciliation select.
	require.NoError(t, r.m.sshMaster.Open(context.Background()))
	select {
	case <-r.m.sshMaster.Changes():
	default:
		t.Fatal("startup notification missing")
	}
	r.start()
	r.awaitStream(t)
	require.Eventually(t, func() bool {
		actual := r.m.state.GetByContainer(discoveryA)
		return len(actual) == 1 && actual[0].Status == "conflict"
	}, 4*time.Second, 10*time.Millisecond, "initial conflict burst must finish")
	created := r.m.state.GetByContainer(discoveryA)[0].CreatedAt
	require.Eventually(t, func() bool { return !r.m.hasUnapplied() }, time.Second, time.Millisecond, "failed Apply still acknowledges discovery request")
	select {
	case <-r.m.sshMaster.Changes():
		t.Fatal("unexpected generation notification before release")
	default:
	}
	require.NoError(t, os.Remove(release))
	// No event, desired mutation or manual reconciliation. This is before the
	// unchanged ten-second inventory audit; only resource retry can repair it.
	require.Eventually(t, func() bool {
		actual := r.m.state.GetByContainer(discoveryA)
		return len(actual) == 1 && actual[0].Status == "active"
	}, 2500*time.Millisecond, 10*time.Millisecond, "RELEASED_CONFLICT_MUST_RETRY_WITHOUT_EVENT")
	require.Equal(t, created, r.m.state.GetByContainer(discoveryA)[0].CreatedAt)
	require.Empty(t, r.m.history.GetAll())
}
