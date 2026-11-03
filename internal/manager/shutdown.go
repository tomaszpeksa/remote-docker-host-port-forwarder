package manager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const shutdownBudget = 10 * time.Second

// ShutdownError marks cleanup failure, including cleanup cancellation. Its safe
// presentation does not expose private child, namespace or publication causes.
type ShutdownError struct{ cause error }

func (e *ShutdownError) Error() string { return "shutdown cleanup failed" }
func (e *ShutdownError) Unwrap() error { return e.cause }

type managerShutdown struct {
	mu                sync.Mutex
	ctx               context.Context
	cancel            context.CancelFunc
	parentStop        func() bool
	parentDone        chan struct{}
	openDone, runDone chan struct{}
	done              chan struct{}
	deadline          time.Time
	err               error
	publishers        func() error
	publicationErr    error
}

func (m *Manager) bindLifetimeLocked(parent context.Context) {
	s := &m.shutdown
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
	}
	if s.parentStop == nil {
		s.parentDone = make(chan struct{})
		s.parentStop = context.AfterFunc(parent, func() { defer close(s.parentDone); m.requestShutdown() })
	}
}

// Open is the one-time CLI startup path. Direct/preopened Run retains its
// constructor timestamp and inventory-before-connection-gate ordering.
func (m *Manager) Open(parent context.Context) (err error) {
	s := &m.shutdown
	s.mu.Lock()
	if s.done != nil || s.openDone != nil || s.runDone != nil {
		s.mu.Unlock()
		return fmt.Errorf("manager startup unavailable")
	}
	s.openDone = make(chan struct{})
	m.bindLifetimeLocked(parent)
	ctx, done := s.ctx, s.openDone
	s.mu.Unlock()
	defer func() {
		close(done)
		if err != nil {
			m.requestShutdown()
		}
	}()
	if parent.Err() != nil {
		m.requestShutdown()
		return parent.Err()
	}
	if err = m.sshMaster.Open(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return context.Canceled
	}
	m.startedAt = time.Now()
	m.metrics.mu.Lock()
	m.metrics.startTime = m.startedAt
	m.metrics.mu.Unlock()
	return nil
}

func (m *Manager) beginRun(parent context.Context) (context.Context, chan struct{}, error) {
	s := &m.shutdown
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil || s.runDone != nil {
		return nil, nil, fmt.Errorf("manager monitoring unavailable")
	}
	s.runDone = make(chan struct{})
	m.bindLifetimeLocked(parent)
	return s.ctx, s.runDone, nil
}

// requestShutdown is the first-terminal ordering point. No logging, waits or
// child I/O precede fencing/cancellation and the immutable monotonic deadline.
func (m *Manager) requestShutdown() {
	s := &m.shutdown
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done != nil {
		return
	}
	m.stopping.Store(true)
	s.deadline = time.Now().Add(shutdownBudget)
	s.done = make(chan struct{})
	if s.cancel != nil {
		s.cancel()
	}
	go m.finalizeShutdown()
}

// Shutdown requests and joins the same finalizer on every call. Expiry fixes a
// failure outcome, never permission to abandon original Wait/worker ownership.
func (m *Manager) Shutdown() (time.Time, error) {
	m.requestShutdown()
	s := &m.shutdown
	<-s.done
	return s.deadline, s.err
}

func (m *Manager) finalizeShutdown() {
	s := &m.shutdown
	ctx, cancel := context.WithDeadline(context.Background(), s.deadline)
	defer cancel()
	retired, err := m.sshMaster.CloseContext(ctx)
	for !retired {
		// An unproved original Wait is still this frame's obligation.
		time.Sleep(100 * time.Millisecond)
		var next error
		retired, next = m.sshMaster.CloseContext(ctx)
		if err == nil {
			err = next
		}
	}
	// Run publishes its worker-joined barrier BEFORE awaiting Shutdown.
	if s.openDone != nil {
		<-s.openDone
	}
	if s.runDone != nil {
		<-s.runDone
	}
	err = errors.Join(err, s.publicationErr)
	m.reconciler.FinalizeShutdown()
	if s.publishers != nil {
		err = errors.Join(err, s.publishers())
	}
	if s.parentStop != nil && !s.parentStop() {
		<-s.parentDone
	}
	if !time.Now().Before(s.deadline) {
		err = errors.Join(err, context.DeadlineExceeded)
	}
	if err != nil {
		s.err = &ShutdownError{cause: err}
	}
	close(s.done)
}
