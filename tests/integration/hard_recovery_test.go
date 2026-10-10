//go:build rdhpf_integration

package integration

import (
	"context"
	"testing"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

func TestOwnedChildHardLossRecovery(t *testing.T) {
	runTwoLossRecovery(t, func(ctx context.Context, m *ssh.Master, _ string) (uint64, int, error) {
		generation, pid, err := m.TestKillOwnedChild(ctx)
		if err == nil {
			t.Logf("STALE_OWNED_SOCKET_AFTER_SIGKILL: generation=%d pid=%d path=%s", generation, pid, m.ControlPath())
		}
		return generation, pid, err
	})
	t.Log("TWO_HARD_OWNED_CHILD_LOSSES_PROVED: retained handles, stale sockets, automatic HTTP and monitoring")
}
