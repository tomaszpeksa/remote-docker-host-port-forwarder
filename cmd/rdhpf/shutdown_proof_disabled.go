//go:build !rdhpf_integration

package main

import (
	"log/slog"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
)

// Released binaries contain no shutdown test controls or environment handling.
func withShutdownProofLogger(logger *slog.Logger) *slog.Logger { return logger }
func bindShutdownProof(_ *slog.Logger, _ *manager.Manager)     {}
