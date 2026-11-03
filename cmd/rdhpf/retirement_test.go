package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWrappedStartupCancellation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		graceful bool
	}{
		{"plain", context.Canceled, true},
		{"wrapped", fmt.Errorf("startup: %w", context.Canceled), true},
		{"joined-cancellation", errors.Join(context.Canceled, fmt.Errorf("startup: %w", context.Canceled)), true},
		{"mixed", errors.Join(context.Canceled, io.ErrUnexpectedEOF), false},
		{"cleanup-cancellation", &cleanupFailure{cause: context.Canceled}, false},
		{"deadline", context.DeadlineExceeded, false},
		{"primary", io.ErrUnexpectedEOF, false},
	} {
		t.Run(tt.name, func(t *testing.T) { require.Equal(t, tt.graceful, cancellationOnly(tt.err)) })
	}
}

type heldSignalLog struct{ entered, release chan struct{} }

func (h heldSignalLog) Enabled(context.Context, slog.Level) bool { return true }
func (h heldSignalLog) Handle(context.Context, slog.Record) error {
	close(h.entered)
	<-h.release
	return nil
}
func (h heldSignalLog) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h heldSignalLog) WithGroup(string) slog.Handler      { return h }

func TestSignalWorkerCancelsBeforeLoggingAndJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		logs := heldSignalLog{entered: make(chan struct{}), release: make(chan struct{})}
		signals := make(chan os.Signal, 3)
		join := startSignalWorker(ctx, cancel, signals, slog.New(logs))
		var once sync.Once
		release := func() { once.Do(func() { close(logs.release) }) }
		t.Cleanup(func() { release(); cancel(); join() })
		signals <- syscall.SIGTERM
		<-logs.entered
		require.ErrorIs(t, ctx.Err(), context.Canceled, "signal must cancel before a noncooperative log callback")
		joined := make(chan struct{})
		go func() { join(); close(joined) }()
		signals <- os.Interrupt
		signals <- syscall.SIGTERM
		synctest.Wait()
		select {
		case <-joined:
			t.Fatal("signal join abandoned its active callback")
		default:
		}
		release()
		<-joined
	})
}

func TestSignalWorkerJoinsWithoutSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	join := startSignalWorker(ctx, cancel, make(chan os.Signal), slog.New(slog.NewTextHandler(io.Discard, nil)))
	cancel()
	join()
}

func TestOuterShutdownDeadlineIncludesAdmissionAndSignals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		primary := fmt.Errorf("startup: %w", context.Canceled)
		deadline := time.Now().Add(10 * time.Second)
		require.Same(t, primary, checkShutdownDeadline(deadline, primary))
		time.Sleep(10 * time.Second)
		err := checkShutdownDeadline(deadline, primary)
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.False(t, cancellationOnly(err), "late admission/signal completion must be exit1")
	})
}
