//go:build rdhpf_integration

package ssh

import (
	"context"
	"fmt"
	"os"
	"time"
)

// TestWaitReceipt captures the retained child, not a reported numeric PID. Call
// its result only after owner cleanup; it waits for the original command Wait.
func (m *Master) TestWaitReceipt() func() (int, error, *os.ProcessState) {
	m.mu.Lock()
	owned := m.connection
	m.mu.Unlock()
	return func() (int, error, *os.ProcessState) {
		if owned == nil {
			return 0, fmt.Errorf("no retained child"), nil
		}
		<-owned.done
		return owned.cmd.Process.Pid, owned.waitErr, owned.cmd.ProcessState
	}
}

// TestKillOwnedChild exists only in the integration test build, never in public
// binaries. It faults the retained handle under the real gate; it does not
// request recovery, publish retirement or signal a mux-reported numeric PID.
func (m *Master) TestKillOwnedChild(ctx context.Context) (uint64, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var generation uint64
	var pid int
	err := m.WithConnection(ctx, func(g uint64, ready bool) error {
		if !ready {
			return fmt.Errorf("owned master not ready for fault")
		}
		c := m.connection
		generation = g
		pid = c.cmd.Process.Pid
		if err := c.cmd.Process.Kill(); err != nil {
			return err
		}
		select {
		case <-c.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		if c.cmd.ProcessState == nil {
			return fmt.Errorf("faulted child was not reaped")
		}
		info, err := os.Lstat(m.controlPath)
		if err != nil {
			return fmt.Errorf("SIGKILL did not leave the required stale endpoint: %w", err)
		}
		if info.Mode()&os.ModeSocket == 0 || c.socket == nil || !os.SameFile(info, c.socket) {
			return fmt.Errorf("stale endpoint differs from associated owned socket")
		}
		return nil
	})
	return generation, pid, err
}
