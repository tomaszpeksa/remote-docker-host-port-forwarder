package logging

import (
	"context"
	"github.com/stretchr/testify/require"
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestParseLevelStrict(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  slog.Level
		bad   bool
	}{
		{"trace", -8, false}, {"TrAcE", -8, false}, {"DEBUG", -4, false}, {"info", 0, false}, {"WARN", 4, false}, {"error", 8, false},
		{"", 0, true}, {" info", 0, true}, {"info ", 0, true}, {"warning", 0, true}, {"private-payload", 0, true},
	} {
		t.Run(tc.input, func(t *testing.T) {
			level, err := ParseLevel(tc.input)
			if tc.bad {
				require.Error(t, err)
				require.EqualError(t, err, "log level must be one of: trace, debug, info, warn, error")
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, level)
			}
		})
	}
}

func TestNewLoggerThresholds(t *testing.T) {
	for _, level := range []slog.Level{LevelTrace, slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		logger := NewLogger(level)
		for _, candidate := range []slog.Level{LevelTrace, slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
			require.Equal(t, candidate >= level, logger.Enabled(context.Background(), candidate))
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "log")
	require.NoError(t, err)
	defer f.Close()
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old }()
	NewLogger(LevelTrace).Log(context.Background(), LevelTrace, "safe operation", "port", 8080)
	_, err = f.Seek(0, 0)
	require.NoError(t, err)
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	require.Contains(t, string(data), "level=TRACE")
	require.Contains(t, string(data), "port=8080")
	require.Contains(t, string(data), "safe operation")
}
