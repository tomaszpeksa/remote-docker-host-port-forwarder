//go:build rdhpf_integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/manager"
)

// These three concrete diagnostic boundaries already execute synchronously in
// production. Only this tagged CLI can hold them; release builds have no control
// socket, fault dispatch, environment parsing or replacement startup hook.
// A retirement-log hold is AFTER the original SSH Wait, not a held Wait itself.
type shutdownProofHandler struct {
	slog.Handler
	proof *shutdownProof
}
type shutdownProof struct {
	mode, socket                 string
	manager                      atomic.Pointer[manager.Manager]
	startup, running, retirement atomic.Bool
}
type shutdownProofReceipt struct {
	Phase                  string
	CLI, Child             int
	Generation             uint64
	Deadline               time.Time
	Stopping, OriginalWait bool
	Error                  string
}

func withShutdownProofLogger(logger *slog.Logger) *slog.Logger {
	proof := &shutdownProof{mode: os.Getenv("RDHPF_S13_PROOF_MODE"), socket: os.Getenv("RDHPF_S13_PROOF_SOCKET")}
	return slog.New(&shutdownProofHandler{Handler: logger.Handler(), proof: proof})
}
func bindShutdownProof(logger *slog.Logger, m *manager.Manager) {
	if handler, ok := logger.Handler().(*shutdownProofHandler); ok {
		handler.proof.manager.Store(m)
	}
}
func (h *shutdownProofHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &shutdownProofHandler{Handler: h.Handler.WithAttrs(attrs), proof: h.proof}
}
func (h *shutdownProofHandler) WithGroup(name string) slog.Handler {
	return &shutdownProofHandler{Handler: h.Handler.WithGroup(name), proof: h.proof}
}
func (h *shutdownProofHandler) Handle(ctx context.Context, record slog.Record) error {
	if err := h.Handler.Handle(ctx, record); err != nil {
		return err
	}
	p := h.proof
	phase := ""
	switch {
	case p.mode == "startup-open" && record.Message == "SSH ControlMaster established":
		child := false
		record.Attrs(func(a slog.Attr) bool { child = child || a.Key == "pid"; return true })
		if child && p.startup.CompareAndSwap(false, true) {
			phase = "startup-open"
		}
	case p.mode == "startup-run" && record.Message == "starting manager":
		if p.startup.CompareAndSwap(false, true) {
			phase = "startup-run"
		}
	case p.mode == "recovery" && record.Message == "manager event loop started":
		if p.running.CompareAndSwap(false, true) {
			phase = "running"
		}
	case (p.mode == "held-stop" || p.mode == "recovery") && record.Message == "owned SSH child retired":
		if p.retirement.CompareAndSwap(false, true) {
			phase = "retirement-completion"
		}
	}
	if phase == "" {
		return nil
	}
	conn, err := net.DialTimeout("unix", p.socket, 2*time.Second)
	if err != nil {
		return fmt.Errorf("tagged shutdown checkpoint: %w", err)
	}
	defer conn.Close()
	encode, decode := json.NewEncoder(conn), json.NewDecoder(conn)
	receipt := func() shutdownProofReceipt {
		deadline, stopping := p.manager.Load().TestShutdownState()
		return shutdownProofReceipt{Phase: phase, CLI: os.Getpid(), Deadline: deadline, Stopping: stopping}
	}
	if err := encode.Encode(receipt()); err != nil {
		return err
	}
	for {
		var command string
		if err := decode.Decode(&command); err != nil {
			return err
		}
		switch command {
		case "release":
			return nil
		case "snapshot":
			if err := encode.Encode(receipt()); err != nil {
				return err
			}
		case "fault-retained-master":
			r := receipt()
			var err error
			r.Generation, r.Child, r.OriginalWait, err = p.manager.Load().TestKillOwnedMaster(context.Background())
			if err != nil {
				r.Error = err.Error()
			}
			return encode.Encode(r)
		default:
			return fmt.Errorf("unknown tagged shutdown command")
		}
	}
}
