package manager

import (
	"context"
	"errors"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

type snapshotWriter interface {
	Write([]state.ForwardState, []state.HistoryEntry) error
	Close() error
}

// startPublishers owns only publication/pathname workers. The caller keeps
// admission until this stop function returns and all remaining cleanup finishes.
func (m *Manager) startPublishers(ctx context.Context) func() error {
	ctx, cancel := context.WithCancel(ctx)
	writerDone := make(chan struct{})
	var writerErr, serverErr error
	go func() { defer close(writerDone); writerErr = m.startStateWriter(ctx) }()
	serverDone := make(chan struct{})
	if m.socketServer == nil {
		close(serverDone)
	} else {
		go func() {
			defer close(serverDone)
			serverErr = m.socketServer.Start(ctx)
		}()
	}
	return func() error {
		cancel()
		<-writerDone
		var closeErr error
		if m.socketServer != nil {
			closeErr = m.socketServer.Close()
		}
		<-serverDone
		return errors.Join(writerErr, serverErr, closeErr, m.stateWriter.Close())
	}
}
