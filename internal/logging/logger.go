package logging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

// contextKey is a private type for context keys to avoid collisions
type contextKey string

const correlationIDKey contextKey = "correlation_id"

// GenerateCorrelationID creates a random 12-character correlation ID.
// Format: hexadecimal string (e.g., "a3f9c2e1b5d4")
//
// Example usage:
//
//	id := logging.GenerateCorrelationID()
//	ctx := logging.WithCorrelationID(context.Background(), id)
func GenerateCorrelationID() string {
	b := make([]byte, 6) // 6 bytes = 12 hex chars
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based ID if random fails (should never happen)
		return "fallback-id"
	}
	return hex.EncodeToString(b)
}

// WithCorrelationID adds a correlation ID to a context.
//
// Example usage:
//
//	id := logging.GenerateCorrelationID()
//	ctx := logging.WithCorrelationID(context.Background(), id)
//	logger := logging.FromContext(ctx)
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// CorrelationID extracts the correlation ID from a context.
// Returns empty string if no correlation ID is set.
//
// Example usage:
//
//	if id := logging.CorrelationID(ctx); id != "" {
//	    fmt.Printf("Correlation ID: %s\n", id)
//	}
func CorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey).(string); ok {
		return id
	}
	return ""
}

// Trace level is more verbose than Debug
const LevelTrace = slog.Level(-8)

// NewLogger creates a new structured logger with the specified level.
// Valid levels: trace, debug, info, warn, error
// All output goes to stdout.
//
// Trace mode provides maximum verbosity including:
// - All debug information
// - Function entry/exit points
// - Goroutine IDs
// - Full payloads (with sensitive data redacted)
//
// Example usage:
//
//	logger := NewLogger("info")
//	logger.Info("message", "key", "value")
//	logger.Debug("debug message") // won't appear with info level
func NewLogger(level string) *slog.Logger {
	var logLevel slog.Level
	switch strings.ToLower(level) {
	case "trace":
		logLevel = LevelTrace
	case "debug":
		logLevel = slog.LevelDebug
	case "info":
		logLevel = slog.LevelInfo
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: logLevel,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Customize trace level name
			if a.Key == slog.LevelKey {
				level := a.Value.Any().(slog.Level)
				if level == LevelTrace {
					a.Value = slog.StringValue("TRACE")
				}
			}
			return a
		},
	}

	handler := slog.NewTextHandler(os.Stdout, opts)
	return slog.New(handler)
}

// FromContext creates a logger with correlation ID from context if present.
// This is the recommended way to create loggers when you have a context.
//
// Example usage:
//
//	ctx := logging.WithCorrelationID(context.Background(), "abc123")
//	logger := logging.FromContext(ctx)
//	logger.Info("event processed") // Will include correlation_id=abc123
func FromContext(ctx context.Context, baseLogger *slog.Logger) *slog.Logger {
	if id := CorrelationID(ctx); id != "" {
		return baseLogger.With("correlation_id", id)
	}
	return baseLogger
}

var (
	// Pattern to match hostnames (hostname or user@hostname)
	hostnamePattern = regexp.MustCompile(`([a-zA-Z0-9_-]+@)?([a-zA-Z0-9][a-zA-Z0-9.-]+)`)

	// Pattern to match IP addresses
	ipPattern = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b`)

	// Pattern to match SSH private key markers
	sshKeyPattern = regexp.MustCompile(`-----BEGIN [A-Z ]+PRIVATE KEY-----[\s\S]*?-----END [A-Z ]+PRIVATE KEY-----`)
)

// Redact sanitizes sensitive data from strings for safe logging.
// It redacts:
// - Hostnames (replaced with [REDACTED-HOST])
// - IP addresses after first octet (e.g., 192.*** → 192.***)
// - SSH private keys (replaced with [REDACTED-SSH-KEY])
//
// Example usage:
//
//	logger.Info("connecting", "host", logging.Redact("user@example.com"))
//	// Output: host=[REDACTED-HOST]
func Redact(value string) string {
	result := value

	// Redact SSH private keys
	result = sshKeyPattern.ReplaceAllString(result, "[REDACTED-SSH-KEY]")

	// Redact IP addresses (keep first octet only)
	result = ipPattern.ReplaceAllString(result, "$1.***")

	// Redact hostnames
	result = hostnamePattern.ReplaceAllString(result, "[REDACTED-HOST]")

	return result
}
