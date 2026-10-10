package manager

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
)

type blockedFinalWriter struct {
	real     *statefile.Writer
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (w *blockedFinalWriter) Write(f []state.ForwardState, h []state.HistoryEntry) error {
	close(w.entered)
	<-w.release
	defer close(w.finished)
	return w.real.Write(f, h)
}
func (w *blockedFinalWriter) Close() error { return w.real.Close() }

func TestPublicationJoinsFinalWriterBeforeCleanup(t *testing.T) {
	m, _, _ := recoveryManager(t)
	owner, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	defer owner.Close()
	real, err := statefile.NewWriter(m.cfg.Host, time.Now())
	require.NoError(t, err)
	blocked := &blockedFinalWriter{real: real, entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	m.stateWriter = blocked
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := m.startPublishers(ctx)
	done := make(chan struct{})
	go func() { stop(); _ = owner.Close(); close(done) }()
	<-blocked.entered // actual cancellation reached final Write, held at the barrier
	// This is a bounded negative assertion while a known operation is blocked,
	// not a sleep guessing when startup/IO completed. A missing join releases
	// admission immediately; the real join cannot finish until release closes.
	select {
	case <-done:
		t.Error("cleanup/admission release preceded final publication")
	case <-time.After(200 * time.Millisecond):
	}
	contender, admissionErr := instancepath.Acquire(m.cfg.Host)
	if contender != nil {
		_ = contender.Close()
	}
	if admissionErr == nil {
		t.Error("successor admitted while final publication was blocked")
	}
	close(blocked.release)
	<-done
	<-blocked.finished
	path, err := statefile.GetStateFilePath(m.cfg.Host)
	require.NoError(t, err)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "final publication must finish before deletion")
	successor, err := instancepath.Acquire(m.cfg.Host)
	require.NoError(t, err)
	defer successor.Close()
	next := statefile.NewWriterAtPath(m.cfg.Host, path, time.Unix(123, 0))
	defer next.Close()
	require.NoError(t, next.Write(nil, nil))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, real.Close())
	require.NoError(t, real.Delete())
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after, "old cleanup must not affect admitted successor")
}
