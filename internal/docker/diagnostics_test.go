package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEventReaderSafeDiagnostics(t *testing.T) {
	for _, level := range []slog.Level{-8, -4, 0, 4, 8} {
		t.Run(level.String(), func(t *testing.T) {
			marker := "private-payload"
			body := marker + "\n" + fmt.Sprintf("{\"Action\":\"start\",\"Actor\":{\"ID\":%q}}\n", eventContainerID) + fmt.Sprintf("{\"Action\":\"stop\",\"Actor\":{\"ID\":%q}}\n", strings.Repeat(marker, 6))
			body += "{\"Action\":\"start\",\"Actor\":{\"ID\":\"s3cr3t\"}}\n"
			installFakeSSH(t, body, marker+" unterminated", 7)
			var logs streamLogBuffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level}))
			var events []Event
			result := <-NewEventReader("ssh://private-user@192.0.2.77", "/private-control", logger).Stream(context.Background(), func(_ context.Context, e Event) error { events = append(events, e); return nil })
			require.Len(t, events, 2, "diagnostic omission must not drop accepted noncanonical IDs")
			var exit *exec.ExitError
			require.ErrorAs(t, result.Err, &exit)
			require.Equal(t, 7, exit.ExitCode())
			require.Equal(t, uint64(2), result.Rejected)
			records := logs.records(t)
			text := fmt.Sprint(records)
			for _, s := range []string{marker, "private-payl", "s3cr3t", "private-user", "192.0.2.77", "/private-control", eventContainerID} {
				require.NotContains(t, text, s)
			}
			require.Contains(t, text, "docker events command failed")
			require.Contains(t, text, "exitCode:7")
			if level <= 0 {
				require.Contains(t, text, "events_received:4")
				require.Contains(t, text, "rejected:2")
				require.Contains(t, text, "pid:")
				require.Contains(t, text, "total_duration_ms:")
			}
			if level <= -4 {
				require.Contains(t, text, eventContainerID[:12])
			}
		})
	}
}

func TestEventReaderPrivateDeliveryCause(t *testing.T) {
	installFakeSSH(t, fmt.Sprintf("{\"Action\":\"start\",\"Actor\":{\"ID\":%q}}\n", eventContainerID), "", 0)
	var logs streamLogBuffer
	cause := errors.New("private-wrapped-payload")
	result := <-NewEventReader("ssh://fixture.invalid", "/private-control", slog.New(slog.NewJSONHandler(&logs, nil))).Stream(context.Background(), func(context.Context, Event) error { return fmt.Errorf("private-wrapper: %w", cause) })
	require.ErrorIs(t, result.Err, cause)
	text := fmt.Sprint(logs.records(t))
	require.NotContains(t, text, "private-")
	require.Contains(t, text, "docker events command failed")
}
