//go:build rdhpf_integration

package manager

import (
	"context"
	"fmt"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
)

func TestManagerForwardingOwnedUnavailableRetirement(t *testing.T) {
	h := newDiscoveryOwned(t)
	target, sibling := h.backend(), h.backend()
	initialList := h.m.discovery.list
	var lists atomic.Int64
	h.m.discovery.list = func(ctx context.Context) ([]string, error) {
		if lists.Add(1) == 1 {
			return initialList(ctx)
		}
		return nil, fmt.Errorf("owned transition inventory withheld after initial observation")
	}
	// This joined, nonterminal fixture stream deliberately delivers no desired
	// events. It isolates reconciliation from stream-driven EnsureAlive repair.
	streamStarted, streamJoined := make(chan struct{}), make(chan struct{})
	h.m.discovery.openStream = func(ctx context.Context, _ func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		terminal := make(chan docker.StreamResult, 1)
		close(streamStarted)
		go func() {
			defer close(terminal)
			defer close(streamJoined)
			<-ctx.Done()
			terminal <- docker.StreamResult{Canceled: true}
		}()
		return terminal
	}
	h.start()
	<-streamStarted
	h.m.sshMaster.StopHealthMonitor()
	t.Log("S12_TRANSITION_ISOLATION health monitor joined; nonterminal joined fixture stream delivers zero desired events")
	h.wait("S12_TRANSITION_INITIAL_HTTP", func() error {
		if h.m.hasUnapplied() {
			return fmt.Errorf("initial desired request still owed")
		}
		if err := h.active(target); err != nil {
			return err
		}
		return h.active(sibling)
	})
	created := h.m.state.GetByContainer(target.id)[0].CreatedAt
	siblingCreated := h.m.state.GetByContainer(sibling.id)[0].CreatedAt
	desired := h.m.state.GetDesired()
	require.ErrorContains(t, h.m.sshMaster.TestRetireExitedChild(h.ctx), "still live")
	require.NoError(t, h.active(target), "live-child refusal must not signal")
	require.NoError(t, h.active(sibling))
	oldWait := h.m.sshMaster.TestWaitReceipt()
	generation, pid, err := h.m.sshMaster.TestKillOwnedChild(h.ctx)
	require.NoError(t, err)
	require.NoError(t, h.m.sshMaster.TestRetireExitedChild(h.ctx))
	h.wait("S12_RETIREMENT_INVALIDATES_WHILE_UNAVAILABLE", func() error {
		if len(h.m.state.GetResources()) != 0 || !h.m.hasUnapplied() {
			return fmt.Errorf("retired authority or missing restoration request")
		}
		return nil
	})
	require.NoError(t, h.m.sshMaster.WithConnection(h.ctx, func(current uint64, ready bool) error {
		require.Equal(t, generation+1, current)
		require.False(t, ready)
		return nil
	}))
	waitPID, waitErr, processState := oldWait()
	require.Equal(t, pid, waitPID)
	require.NotNil(t, processState)
	var status syscall.WaitStatus
	_, err = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	require.ErrorIs(t, err, syscall.ECHILD)
	t.Logf("S12_TRANSITION_ORIGINAL_WAIT pid=%d Wait=%v ProcessState=%v ECHILD=%v unavailable_generation=%d", pid, waitErr, processState, err, generation+1)
	require.Empty(t, h.m.history.GetAll())
	// Fresh backend content excludes a stale HTTP observation after replacement.
	for _, backend := range []*discoveryBackend{&target, &sibling} {
		backend.body += "-after-unavailable"
		h.f.Docker("exec", backend.id, "sh", "-c", "printf %s "+backend.body+" > /usr/share/nginx/html/index.html")
	}
	require.NoError(t, h.m.sshMaster.EnsureAlive(h.ctx))
	forwardingRetirement(t, h)
	require.NoError(t, h.m.sshMaster.WithConnection(h.ctx, func(current uint64, ready bool) error {
		require.Equal(t, generation+1, current)
		require.True(t, ready)
		return nil
	}))
	h.wait("S12_RETIRED_UNAVAILABLE_SAME_GENERATION_HTTP", func() error {
		if err := h.active(target); err != nil {
			return err
		}
		return h.active(sibling)
	})
	require.NoError(t, forwardingHTTP(h, target))
	require.NoError(t, forwardingHTTP(h, sibling))
	require.ElementsMatch(t, desired, h.m.state.GetDesired())
	require.Equal(t, created, h.m.state.GetByContainer(target.id)[0].CreatedAt)
	require.Equal(t, siblingCreated, h.m.state.GetByContainer(sibling.id)[0].CreatedAt)
	require.Empty(t, h.m.history.GetAll())
	t.Logf("S12_UNAVAILABLE_RESTORATION generation=%d list_calls=%d delivered_events=0 manual_Apply=0; controlled retired/unavailable transition, not native replacement-start failure", generation+1, lists.Load())
	h.close()
	<-streamJoined
	t.Log("S12_TRANSITION_STREAM_JOINED before dependent fixture cleanup")
}
