package integration

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

func newOwnedMaster(t *testing.T, host string, logger *slog.Logger) (*ssh.Master, error) {
	t.Helper()
	master, err := ssh.NewMaster(host, logger)
	if err == nil {
		t.Cleanup(func() { require.NoError(t, master.Close()) })
	}
	return master, err
}

func admitPublisher(t *testing.T, host string) *instancepath.Owner {
	t.Helper()
	owner, err := instancepath.Acquire(host)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	return owner
}

func runOwnedManager(t *testing.T, ctx context.Context, mgr *manager.Manager, owner *instancepath.Owner) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx, owner) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("owned Manager cleanup: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("owned Manager did not join")
		}
	})
}
