//go:build rdhpf_integration

package manager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
)

func TestAcceptedEventReceiptSurvivesEmptyOwnerPruning(t *testing.T) {
	r := newWatchdogRun(t)
	id := strings.Repeat("a", 64)
	r.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		terminal := make(chan docker.StreamResult, 1)
		go func() {
			err := deliver(ctx, docker.Event{Type: "stop", ContainerID: id})
			if err == nil {
				<-ctx.Done()
			}
			terminal <- docker.StreamResult{Err: err, Canceled: true}
			close(terminal)
		}()
		return terminal
	}
	accepted := make(chan docker.Event, 1)
	r.m.TestObserveAcceptedEvents(func(event docker.Event) { accepted <- event })
	r.start()
	select {
	case event := <-accepted:
		require.Equal(t, id, event.ContainerID)
	case <-time.After(time.Second):
		t.Fatal("full-ID accepted-event setup receipt missing")
	}
	require.Eventually(t, func() bool {
		r.m.discovery.mu.Lock()
		defer r.m.discovery.mu.Unlock()
		return r.m.discovery.pending == 0
	}, time.Second, time.Millisecond)
	require.Empty(t, r.m.state.GetDesired(), "accepted setup receipt must not depend on an empty tombstone")
	require.Empty(t, r.m.state.GetActual())
	require.Empty(t, r.m.state.GetResources())
}

func TestAcceptedEventObserverRejectsFailedDelivery(t *testing.T) {
	r := newWatchdogRun(t)
	r.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		done := make(chan docker.StreamResult, 1)
		done <- docker.StreamResult{Err: deliver(ctx, docker.Event{ContainerID: strings.Repeat("b", 64)})}
		close(done)
		return done
	}
	observed := false
	r.m.TestObserveAcceptedEvents(func(docker.Event) { observed = true })
	result := <-r.m.discovery.openStream(context.Background(), func(context.Context, docker.Event) error { return context.Canceled })
	require.ErrorIs(t, result.Err, context.Canceled)
	require.False(t, observed, "rejected delivery is not an accepted setup event")
}
