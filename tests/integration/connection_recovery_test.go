package integration

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// TestConnectionLossRecovery restores actual application traffic after two
// distinct owned mux losses. It never requests recovery or edits actual state.
func TestConnectionLossRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("disruptive recovery integration")
	}
	host, token := recoveryAlias(t, getTestSSHHost(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ports := []int{18181, 18182}
	contents := make(map[int]string)
	ownedIDs := make([]string, 0, 2)
	for i, port := range ports {
		id, content := recoveryBackend(t, ctx, host, token, i, port)
		ownedIDs = append(ownedIDs, id)
		contents[port] = content
	}
	var checks atomic.Uint64
	observer := recoveryObserver{Handler: slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}), checks: &checks, stream: make(chan struct{}), once: &sync.Once{}}
	logger := slog.New(observer)
	master, err := ssh.NewMaster(host, logger)
	require.NoError(t, err)
	_, err = os.Lstat(master.ControlPath())
	require.True(t, os.IsNotExist(err), "test mux endpoint must be unused")
	t.Cleanup(func() { require.NoError(t, master.Close()) })
	require.NoError(t, master.Open(ctx)) // exercises authenticated kernel/mux association
	st, history := state.NewState(), state.NewHistory()
	mgr := manager.NewManager(&config.Config{Host: host}, docker.NewEventReader(host, master.ControlPath(), logger), reconcile.NewReconciler(st, history, logger), master, st, history, logger)
	managerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	var runErr error
	go func() { runErr = mgr.Run(managerCtx); close(done) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
			require.NoError(t, runErr)
		case <-time.After(15 * time.Second):
			t.Error("owned Manager did not finish cleanup")
		}
	})
	select {
	case <-observer.stream:
	case <-done:
		t.Fatalf("Manager exited: %v", runErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	transport := &http.Transport{DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	t.Cleanup(transport.CloseIdleConnections)
	assertHTTP := func() {
		bounded, finish := context.WithTimeout(ctx, 60*time.Second)
		defer finish()
		recoveryAwait(t, bounded, done, "HTTP content on every desired port", func() error {
			for _, port := range ports {
				if err := recoveryHTTP(client, port, contents[port]); err != nil {
					return err
				}
			}
			return nil
		})
		for _, port := range ports {
			require.NoError(t, recoveryHTTP(client, port, contents[port]))
			t.Logf("fresh HTTP200 content verified port=%d body=%q", port, contents[port])
		}
	}
	assertHTTP()
	// Startup inventory currently uses short IDs; preserve that contract, not
	// the deferred canonical-ID/resync policy. Each requested owner must be ours.
	desired := st.GetDesired()
	require.Len(t, desired, 2)
	for _, entry := range desired {
		found := false
		for _, id := range ownedIDs {
			if strings.HasPrefix(id, entry.ContainerID) {
				found = true
			}
		}
		require.True(t, found, "desired owner must be one of the two owned backends")
	}
	created := make(map[int]time.Time)
	for _, forward := range st.GetActual() {
		created[forward.Port] = forward.CreatedAt
	}
	require.Len(t, created, 2)
	generation, pid, err := recoveryMux(ctx, master, host, false)
	require.NoError(t, err)
	t.Logf("owned association ready host=%s path=%s generation=%d pid=%d", host, master.ControlPath(), generation, pid)
	waitMonitor := func() {
		previous := checks.Load()
		bounded, finish := context.WithTimeout(ctx, 25*time.Second)
		defer finish()
		recoveryAwait(t, bounded, done, "continued automatic monitor check", func() error {
			if checks.Load() <= previous {
				return fmt.Errorf("no new health check after %d", previous)
			}
			return nil
		})
		t.Logf("automatic monitor continued checks=%d", checks.Load())
	}
	for loss := 1; loss <= 2; loss++ {
		lostGeneration, lostPID, err := recoveryMux(ctx, master, host, true)
		require.NoError(t, err)
		require.Equal(t, generation, lostGeneration)
		require.Equal(t, pid, lostPID)
		t.Logf("forced owned loss=%d acknowledged under gate path=%s generation=%d pid=%d", loss, master.ControlPath(), lostGeneration, lostPID)
		recovered, finish := context.WithTimeout(ctx, 60*time.Second)
		recoveryAwait(t, recovered, done, "distinct automatically recovered master", func() error {
			generation, pid, err = recoveryMux(recovered, master, host, false)
			if err != nil {
				return err
			}
			if generation <= lostGeneration || pid == lostPID {
				return fmt.Errorf("old master still observed: generation=%d pid=%d", generation, pid)
			}
			return nil
		})
		finish()
		assertHTTP()
		// Pings may add unrelated empty desired entries; the two original desired
		// container endpoints must remain unchanged throughout recovery.
		current := st.GetDesired()
		for _, original := range desired {
			require.Contains(t, current, original)
		}
		for _, forward := range st.GetActual() {
			if before, ok := created[forward.Port]; ok {
				require.Equal(t, before, forward.CreatedAt)
			}
		}
		require.Empty(t, history.GetAll(), "reconnect must not create stop history")
		waitMonitor()
	}
	waitMonitor() // another full production health interval after the second loss
	assertHTTP()
	t.Log("TWO_LOSS_HTTP_RECOVERY_PROVED: both ports, distinct masters, desired continuity and monitoring")
}

// TestHealthMonitorPeriodic verifies health checks run at the configured interval
func TestHealthMonitorPeriodic(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)
	if sshHost == "" {
		t.Skip("TEST_SSH_HOST not set, skipping SSH integration test")
	}

	t.Skip("Health monitoring not yet implemented - will be added in T061")

	// Expected implementation:
	// 1. Set up log capture to count health checks
	// 2. Start health monitor with 1s interval
	// 3. Run for 5 seconds
	// 4. Verify ~5 health checks occurred (±1 for timing variance)
}

// TestRecoveryRestoresAllForwards verifies that after SSH recovery,
// all previously established forwards are restored.
func TestRecoveryRestoresAllForwards(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)
	if sshHost == "" {
		t.Skip("TEST_SSH_HOST not set, skipping SSH integration test")
	}

	t.Skip("Full recovery flow not yet implemented - will be added in T061/T062")

	// Expected implementation:
	// 1. Start multiple containers with different ports
	// 2. Verify all forwards established
	// 3. Kill SSH master
	// 4. Wait for recovery
	// 5. Verify all forwards re-established
	// 6. Test connectivity to all ports
}

// TestRecoveryTriggersReconciliation verifies that SSH recovery triggers
// a full reconciliation to ensure state consistency.
func TestRecoveryTriggersReconciliation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)
	if sshHost == "" {
		t.Skip("TEST_SSH_HOST not set, skipping SSH integration test")
	}

	t.Skip("Manager auto-recovery integration not yet implemented - will be added in T062")

	// Expected implementation:
	// 1. Run full manager with Docker events
	// 2. Start container, verify forward established
	// 3. Kill SSH master
	// 4. Capture logs to verify "SSH connection recovered, reconciling state"
	// 5. Verify forward restored
	// 6. Verify container state preserved
}

// TestEventStreamAutoRestart verifies Docker event stream restarts on errors
func TestEventStreamAutoRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	sshHost := getTestSSHHost(t)
	if sshHost == "" {
		t.Skip("TEST_SSH_HOST not set, skipping SSH integration test")
	}

	t.Skip("Event stream auto-restart testing requires stream interruption simulation")

	// Expected implementation:
	// 1. Start event stream
	// 2. Simulate stream failure (kill docker events process or network disruption)
	// 3. Verify stream restarts with backoff
	// 4. Verify events continue to be processed
	// 5. Verify reconciliation triggered after restart
}

// TestMaxRecoveryAttempts verifies the tool gives up after max recovery attempts
func TestMaxRecoveryAttempts(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Recovery attempt limiting not yet implemented - will be added in T061/T068")

	// Expected implementation:
	// 1. Configure health monitor with low max attempts (e.g., 3)
	// 2. Simulate persistent SSH failure (wrong credentials, host down, etc.)
	// 3. Verify recovery attempts stop after max attempts
	// 4. Verify appropriate error logged
}

// TestRecoveryBackoffDelays verifies exponential backoff between recovery attempts
func TestRecoveryBackoffDelays(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Skip("Recovery backoff not yet implemented - will be added in T061/T068")

	// Expected implementation:
	// 1. Simulate SSH failures
	// 2. Measure time between recovery attempts
	// 3. Verify delays follow exponential pattern: 1s, 2s, 4s, 8s, ...
	// 4. Verify max delay cap (e.g., 30s)
}
