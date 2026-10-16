package manager

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestManagerDiscoveryRunIndependentOfConnectionGate(t *testing.T) {
	m := inventoryManager(t)
	home, err := os.MkdirTemp("/tmp", "s09-run-")
	require.NoError(t, err)
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	owner, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	defer owner.Close()
	entered, release, gateDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		gateDone <- m.sshMaster.WithConnection(context.Background(), func(uint64, bool) error { close(entered); <-release; return nil })
	}()
	<-entered
	var once sync.Once
	defer once.Do(func() { close(release) })
	audited := make(chan struct{})
	var auditOnce sync.Once
	m.discovery.list = func(context.Context) ([]string, error) { return []string{discoveryA}, nil }
	m.discovery.observe = func(context.Context, string) ([]int, error) { return []int{1001}, nil }
	m.discovery.after = func(d time.Duration) <-chan time.Time { auditOnce.Do(func() { close(audited) }); return time.After(d) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx, owner) }()
	// Join on assertion failure too, before the earlier owner/HOME defers run.
	defer func() {
		cancel()
		once.Do(func() { close(release) })
		gateErr, runErr := <-gateDone, <-done
		require.NoError(t, gateErr)
		require.NoError(t, runErr)
	}()
	select {
	case <-audited:
	case <-time.After(2 * time.Second):
		t.Error("INVENTORY_BLOCKED_BY_CONNECTION_GATE")
	}
	require.Contains(t, m.state.GetDesired(), state.ContainerPorts{ContainerID: discoveryA, Ports: []int{1001}})
	require.Empty(t, m.state.GetByContainer(discoveryA), "inventory must not fabricate actual forwards")
}
