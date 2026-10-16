package manager

import (
	"context"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

type snapshotWriter interface {
	Write([]state.ForwardState, []state.HistoryEntry) error
	Close() error
}

// startPublishers owns only publication/pathname workers. The caller keeps
// admission until this stop function returns and all remaining cleanup finishes.
func (m *Manager) startPublishers(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); m.startStateWriter(ctx) }()
	serverDone := make(chan struct{})
	if m.socketServer == nil {
		close(serverDone)
	} else {
		go func() {
			defer close(serverDone)
			if err := m.socketServer.Start(ctx); err != nil && ctx.Err() == nil {
				m.logger.Warn("socket server error", "error", err)
			}
		}()
	}
	return func() {
		cancel()
		<-writerDone
		if m.socketServer != nil {
			if err := m.socketServer.Close(); err != nil {
				m.logger.Warn("failed to close socket server", "error", err)
			}
		}
		<-serverDone
		if err := m.stateWriter.Close(); err != nil {
			m.logger.Warn("failed to close state writer", "error", err)
		}
	}
}
