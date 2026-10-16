package docker

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEventReaderCleanupOverrunRemainsJoined(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		finished := make(chan struct{})
		done := watchStreamCleanup(ctx, finished, slog.New(slog.NewTextHandler(&logs, nil)))
		defer func() { close(finished); <-done }()
		cancel()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("cleanup must not detach an unfinished owned worker")
		default:
		}
		require.Contains(t, logs.String(), "cleanup allowance exceeded", "CLEANUP_OVERRUN_MUST_BE_DIAGNOSED")
	})
}
