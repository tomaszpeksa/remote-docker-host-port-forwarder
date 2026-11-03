//go:build rdhpf_integration

package manager

import (
	"context"
	"time"
)

// TestShutdownState is an observation of the real first-terminal ordering point.
func (m *Manager) TestShutdownState() (time.Time, bool) {
	m.shutdown.mu.Lock()
	defer m.shutdown.mu.Unlock()
	return m.shutdown.deadline, m.stopping.Load()
}

// TestKillOwnedMaster faults only the retained handle, and observes its original
// Wait. A controller's own child Wait is not this application's SSH retirement.
func (m *Manager) TestKillOwnedMaster(ctx context.Context) (uint64, int, bool, error) {
	wait := m.sshMaster.TestWaitReceipt()
	generation, pid, err := m.sshMaster.TestKillOwnedChild(ctx)
	if err != nil {
		return generation, pid, false, err
	}
	_, _, process := wait()
	return generation, pid, process != nil, nil
}
