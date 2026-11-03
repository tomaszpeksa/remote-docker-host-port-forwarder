package main

import (
	"context"
	"errors"
	"log/slog"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/logging"
)

// cliGuidance separates presentation from private error identity/classification.
// Only fixed application-authored guidance belongs in message.
type cliGuidance struct {
	message string
	cause   error
}

func (e *cliGuidance) Error() string { return e.message }
func (e *cliGuidance) Unwrap() error { return e.cause }

func cliErrorMessage(err error) string {
	if cancellationOnly(err) {
		return "operation canceled: context canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "operation deadline exceeded"
	}
	var guidance *cliGuidance
	if errors.As(err, &guidance) {
		return guidance.message
	}
	return "operation failed; check configuration and local ownership (rdhpf --help)"
}

func resolveRunConfig(host, flagLevel string, levelSupplied, trace bool, envLevel, envLabels string) (*config.Config, slog.Level, error) {
	level := slog.LevelInfo
	selected := "info"
	if levelSupplied {
		parsed, err := logging.ParseLevel(flagLevel)
		if err != nil {
			return nil, 0, &cliGuidance{message: "--log-level must be one of: trace, debug, info, warn, error", cause: err}
		}
		level, selected = parsed, flagLevel
	}
	if envLevel != "" {
		parsed, err := logging.ParseLevel(envLevel)
		if err != nil {
			return nil, 0, &cliGuidance{message: "RDHPF_LOG_LEVEL must be one of: trace, debug, info, warn, error", cause: err}
		}
		level, selected = parsed, envLevel
	}
	if trace {
		level, selected = logging.LevelTrace, "trace"
	}
	enabled := false
	switch envLabels {
	case "", "0":
	case "1":
		enabled = true
	default:
		return nil, 0, &cliGuidance{message: "RDHPF_ENABLE_LABEL_PORTS must be empty, 0 or 1"}
	}
	cfg := &config.Config{Host: host, LogLevel: selected, EnableLabelPorts: enabled}
	if err := cfg.Validate(); err != nil {
		return nil, 0, &cliGuidance{message: "invalid configuration; --host must be an SSH URL (ssh://user@host)", cause: err}
	}
	return cfg, level, nil
}
