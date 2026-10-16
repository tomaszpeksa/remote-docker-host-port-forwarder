package main

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

func TestSSHCleanupRetainsAdmissionUntilLateRetirement(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	owner, err := instancepath.Acquire("ssh://incumbent")
	require.NoError(t, err)
	attempted := make(chan struct{})
	completed := make(chan struct{})
	returned := make(chan struct{})
	var announced, supplied sync.Once
	release := func() { supplied.Do(func() { close(completed) }) }
	defer release()
	go func() {
		defer close(returned)
		defer owner.Close() // same owning order as run: admission released last
		closeSSHMaster(func() error {
			announced.Do(func() { close(attempted) })
			select {
			case <-completed:
				return nil
			default:
				return ssh.ErrRetirementUncertain
			}
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	<-attempted
	select {
	case <-returned:
		t.Error("CLI cleanup abandoned unresolved retirement")
	case <-time.After(200 * time.Millisecond):
	}
	contender, err := instancepath.Acquire("ssh://contender")
	if contender != nil {
		contender.Close()
	}
	require.Error(t, err, "successor must be excluded while completion is unproved")
	release()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("late completion did not release cleanup")
	}
	successor, err := instancepath.Acquire("ssh://successor")
	require.NoError(t, err)
	require.NoError(t, successor.Close())
}

func TestSSHCleanupDoesNotRetryUnrelatedErrors(t *testing.T) {
	calls := 0
	closeSSHMaster(func() error { calls++; return io.ErrUnexpectedEOF }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.Equal(t, 1, calls, "only unproved retirement extends the owning run lifetime")
}
