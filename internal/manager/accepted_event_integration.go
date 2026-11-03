//go:build rdhpf_integration

package manager

import (
	"context"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
)

// TestObserveAcceptedEvents observes successful enqueue only, never completed
// application. Install before Run; this setup receipt exists only in test builds.
func (m *Manager) TestObserveAcceptedEvents(observe func(docker.Event)) {
	stream := m.discovery.openStream
	m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		return stream(ctx, func(ctx context.Context, event docker.Event) error {
			if err := deliver(ctx, event); err != nil {
				return err
			}
			observe(event)
			return nil
		})
	}
}
