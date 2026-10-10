//go:build rdhpf_integration

package ssh

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOwnedFaultSeamMonitorRecoversTwice(t *testing.T) {
	m, dir := masterFixture(t)
	events := masterEvents(t, dir)
	require.NoError(t, m.Open(context.Background()))
	first := awaitMasterEvent(t, events, "start", 0)
	m.StartHealthMonitor(context.Background(), 5*time.Millisecond)
	defer m.StopHealthMonitor()
	for i := 0; i < 2; i++ {
		generation, pid, err := m.TestKillOwnedChild(context.Background())
		require.NoError(t, err)
		require.Equal(t, uint64(i), generation)
		require.Equal(t, first.PID, pid)
		first = awaitMasterEvent(t, events, "start", pid)
		awaitMasterEvent(t, events, "check", pid)
	}
}
