//go:build rdhpf_integration

package manager

import (
	"context"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestShutdownLostReplyConsumesProductionActualOnce(t *testing.T) {
	for _, operation := range []string{"OPEN", "CLOSE"} {
		t.Run(operation, func(t *testing.T) {
			// An actual local listener provides the existing readiness probe; muxstub
			// transports the real OPEN/CLOSE frames but is not application traffic.
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, listener.Close()) })
			r := newWatchdogRun(t)
			producerEntered, producerRelease := make(chan struct{}), make(chan struct{})
			gateEntered, gateRelease := make(chan struct{}), make(chan struct{})
			var producerOnce, gateOnce sync.Once
			unblockProducer := func() { producerOnce.Do(func() { close(producerRelease) }) }
			unblockGate := func() { gateOnce.Do(func() { close(gateRelease) }) }
			var gateDone, waitDone chan struct{}
			t.Cleanup(func() {
				unblockGate()
				unblockProducer()
				if gateDone != nil {
					<-gateDone
				}
				if waitDone != nil {
					<-waitDone
				}
			})
			r.m.discovery.list = func(ctx context.Context) ([]string, error) {
				close(producerEntered)
				<-producerRelease
				return nil, ctx.Err()
			}
			require.NoError(t, r.m.sshMaster.Open(context.Background()))
			id, port := strings.Repeat("c", 64), listener.Addr().(*net.TCPAddr).Port
			r.m.state.SetDesired(id, []int{port})
			cycle := func() error {
				r.m.discovery.mu.Lock()
				r.m.requestReconcile()
				r.m.discovery.mu.Unlock()
				return r.m.triggerReconcile(context.Background())
			}
			if operation == "CLOSE" {
				require.NoError(t, cycle())
				r.m.state.SetDesired(id, nil)
			}
			lost, calls := os.Getenv("S12_MUX_LOST_REPLY"), os.Getenv("S12_MUX_RECORD")
			require.NoError(t, os.WriteFile(lost, nil, 0600))
			require.Error(t, cycle(), "actual mux reply loss must reach production reconciliation")
			resource, ok := r.m.state.Resource(id, port)
			require.True(t, ok)
			require.Equal(t, state.ForwardUnknown, resource.Ownership)
			if operation == "CLOSE" {
				require.Equal(t, "container stopped", resource.EndReason)
			} else {
				require.Empty(t, resource.EndReason)
			}
			actual := r.m.state.GetByContainer(id)
			require.Len(t, actual, 1)
			created := actual[0].CreatedAt
			before, err := os.ReadFile(calls)
			require.NoError(t, err)
			require.Equal(t, 1, strings.Count(string(before), operation), "the uncertain operation must actually have been sent")
			r.start()
			<-producerEntered
			r.awaitStream(t)
			wait := r.m.sshMaster.TestWaitReceipt()
			waitDone = make(chan struct{})
			go func() {
				defer close(waitDone)
				_, _, process := wait()
				if process == nil {
					t.Error("original Wait proof missing")
				}
			}()
			gateDone = make(chan struct{})
			go func() {
				defer close(gateDone)
				_ = r.m.sshMaster.WithConnection(context.Background(), func(uint64, bool) error { close(gateEntered); <-gateRelease; return nil })
			}()
			<-gateEntered
			r.cancel()
			r.m.requestShutdown()
			deadline := r.m.shutdown.deadline
			require.Empty(t, r.m.history.GetAll(), "no history while the Master retirement gate is held")
			require.Len(t, r.m.state.GetActual(), 1)
			require.Len(t, r.m.state.GetResources(), 1)
			unblockGate()
			<-gateDone
			<-waitDone
			// This is now a held Run-owned producer, NOT a held original Wait.
			require.Empty(t, r.m.history.GetAll(), "child retirement alone is not producer quiescence")
			require.Len(t, r.m.state.GetActual(), 1)
			require.Len(t, r.m.state.GetResources(), 1)
			r.m.requestShutdown()
			require.Equal(t, deadline, r.m.shutdown.deadline)
			contender, err := instancepath.Acquire(r.m.cfg.Host)
			if contender != nil {
				_ = contender.Close()
			}
			require.Error(t, err, "admission retained until producer and terminal owner join")
			producerReleaseAt := time.Now()
			unblockProducer()
			<-r.done
			require.NoError(t, r.err)
			history := r.m.history.GetAll()
			require.Len(t, history, 1)
			require.Equal(t, created, history[0].StartedAt)
			require.False(t, history[0].EndedAt.Before(producerReleaseAt), "history follows original retirement and producer release")
			reason := "rdhpf shutdown"
			if operation == "CLOSE" {
				reason = "container stopped"
			}
			require.Equal(t, reason, history[0].EndReason)
			for range 2 {
				later, err := r.m.Shutdown()
				require.NoError(t, err)
				require.Equal(t, deadline, later)
			}
			require.Len(t, r.m.history.GetAll(), 1)
			require.Empty(t, r.m.state.GetActual())
			require.Empty(t, r.m.state.GetResources())
			require.Empty(t, r.m.state.GetDesired())
			after, err := os.ReadFile(calls)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "terminal cleanup/repeats cannot replay or renew OPEN/CLOSE")
			t.Logf("S13_LOST_REPLY operation=%s original-CreatedAt=%s reason=%q original-Wait-and-producer-joined=true history=1", operation, created, reason)
		})
	}
}
