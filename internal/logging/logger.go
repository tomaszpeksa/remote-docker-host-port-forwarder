package logging

import (
	"errors"
	"log/slog"
	"os"
	"strings"
)

// LevelTrace retains the trace threshold; current production content equals debug.
const LevelTrace = slog.Level(-8)

// ParseLevel accepts the existing names case-insensitively, without trimming.
// Empty programmatic settings are defaulted by their consumer, not this parser.
func ParseLevel(level string) (slog.Level, error) {
	switch strings.ToLower(level) {
	case "trace":
		return LevelTrace, nil
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errors.New("log level must be one of: trace, debug, info, warn, error")
	}
}

// NewLogger creates a stdout logger at an already parsed level. Producers omit
// sensitive diagnostic inputs before handing records to any slog handler.
func NewLogger(level slog.Level) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey && a.Value.Any().(slog.Level) == LevelTrace {
				a.Value = slog.StringValue("TRACE")
			}
			return a
		},
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}
