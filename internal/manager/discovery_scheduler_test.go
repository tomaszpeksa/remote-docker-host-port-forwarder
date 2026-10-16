package manager

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestManagerDiscoveryScheduler(t *testing.T) {
	t.Run("TimerOnlyPartialRepair", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			m := inventoryManager(t)
			m.state.SetDesired(discoveryA, []int{1001})
			m.state.SetDesired(discoveryB, []int{2001})
			var empty atomic.Bool
			m.discovery.list = func(context.Context) ([]string, error) {
				if empty.Load() {
					return nil, nil
				}
				return []string{discoveryB, discoveryC}, nil
			}
			var fail atomic.Bool
			fail.Store(true)
			m.discovery.observe = func(ctx context.Context, id string) ([]int, error) {
				if id == discoveryB && fail.Load() {
					return nil, errors.New("B unknown")
				}
				return []int{3001}, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go m.runDiscovery(ctx)
			synctest.Wait()
			require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryA, Ports: []int{}}, "TIMER_ABSENCE_REPAIR")
			require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryC, Ports: []int{3001}}, "PARTIAL_C_PROGRESS")
			require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryB, Ports: []int{2001}})
			fail.Store(false)
			time.Sleep(time.Second)
			synctest.Wait()
			require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryB, Ports: []int{3001}}, "RETRY_WITHOUT_EVENT")
			// Unknown subscription: mutate list only; no event or repair wake.
			empty.Store(true)
			time.Sleep(10 * time.Second)
			synctest.Wait()
			for _, cp := range m.state.GetDesired() {
				require.Empty(t, cp.Ports, "AUDIT_WITHOUT_STREAM")
			}
			cancel()
			synctest.Wait()
		})
	})
	t.Run("EligibilitySurvivesWakeups", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			m := inventoryManager(t)
			var calls atomic.Int64
			m.discovery.list = func(context.Context) ([]string, error) { calls.Add(1); return nil, errors.New("offline") }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go m.runDiscovery(ctx)
			synctest.Wait()
			require.Equal(t, int64(1), calls.Load(), "INITIAL_AUDIT")
			for i := 0; i < 9; i++ {
				time.Sleep(100 * time.Millisecond)
				m.markRepair()
				synctest.Wait()
				require.Equal(t, int64(1), calls.Load())
			}
			time.Sleep(100 * time.Millisecond)
			synctest.Wait()
			require.Equal(t, int64(2), calls.Load(), "ELIGIBILITY_NOT_RESET")
			time.Sleep(14 * time.Second)
			synctest.Wait()
			require.Equal(t, int64(5), calls.Load())
			m.markRepair()
			time.Sleep(time.Second)
			synctest.Wait()
			require.Equal(t, int64(5), calls.Load(), "AUDIT_MUST_NOT_BYPASS_BACKOFF")
			time.Sleep(15 * time.Second)
			synctest.Wait()
			require.Equal(t, int64(6), calls.Load())
			cancel()
			synctest.Wait()
		})
	})
}

func TestManagerDiscoveryAcceptance(t *testing.T) {
	t.Run("PendingEnqueueCancel", func(t *testing.T) {
		m := inventoryManager(t)
		m.discovery.events = make(chan acceptedEvent, 10)
		for i := 0; i < 10; i++ {
			m.discovery.events <- acceptedEvent{}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := m.deliverEvent(ctx, 0, docker.Event{Type: "stop", ContainerID: discoveryA})
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, uint64(1), m.discovery.revision, "RESERVE_BEFORE_CANCELED_ENQUEUE")
		require.Zero(t, m.discovery.pending)
	})
	t.Run("StaleGenerationAcknowledgement", func(t *testing.T) {
		m := inventoryManager(t)
		m.discovery.events = make(chan acceptedEvent, 10)
		require.NoError(t, m.deliverEvent(context.Background(), 0, docker.Event{Type: "stop", ContainerID: discoveryA}))
		accepted := <-m.discovery.events
		require.NotNil(t, accepted.token, "ACCEPTANCE_RESERVATION")
		require.Equal(t, 1, m.discovery.pending)
		m.retireEvent(accepted.token)
		m.retireEvent(accepted.token)
		require.Zero(t, m.discovery.pending, "RETIRE_EXACTLY_ONCE")
		m.discovery.generation = 1
		m.discovery.pending = 2
		m.retireEvent(accepted.token)
		require.Equal(t, 2, m.discovery.pending, "OLD_ACK_CANNOT_RETIRE_NEW")
	})
}
