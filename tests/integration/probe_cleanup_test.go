package integration

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type probeCloseBody struct {
	io.Reader
	closeErr error
	closes   int
}

func (b *probeCloseBody) Close() error {
	b.closes++
	return b.closeErr
}

func TestSuccessfulProbeCloseErrorDoesNotReclassifyResponse(t *testing.T) {
	for _, closeErr := range []error{nil, errors.New("probe-close-control")} {
		name := "clean"
		if closeErr != nil {
			name = "close-error"
		}
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			defer slog.SetDefault(previous)
			body := &probeCloseBody{Reader: bytes.NewReader(nil), closeErr: closeErr}
			response := &http.Response{StatusCode: http.StatusOK, Body: body}
			stats := &LoadStats{TotalRequests: 1, MinLatency: time.Hour}
			stats.recordSuccessfulProbe(response, 5*time.Millisecond)
			require.Equal(t, 1, body.closes, "cleanup must not retry")
			require.Equal(t, 1, stats.TotalRequests)
			require.Equal(t, 1, stats.SuccessCount)
			require.Zero(t, stats.FailureCount)
			require.Equal(t, []time.Duration{5 * time.Millisecond}, stats.Latencies)
			require.Equal(t, 5*time.Millisecond, stats.MinLatency)
			require.Equal(t, 5*time.Millisecond, stats.MaxLatency)
			if closeErr != nil {
				require.Contains(t, logs.String(), "closing successful HTTP probe response")
				require.Contains(t, logs.String(), closeErr.Error())
			} else {
				require.Empty(t, logs.String())
			}
		})
	}
}
