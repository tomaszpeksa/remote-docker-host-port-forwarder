//go:build rdhpf_integration

package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/util"
)

// TestCancelFailure injects only a predispatch failure for the owned target.
// Configure before Run; atomic release changes no desired state or event.
func (r *Reconciler) TestCancelFailure(port int, armed *atomic.Bool, attempts chan<- struct{}) {
	r.cancelForward = func(ctx context.Context, connection *ssh.Forwarding, local, remote int, logger *slog.Logger) (ssh.ForwardEffect, error) {
		if local == port && armed.Load() {
			select {
			case attempts <- struct{}{}:
			default:
			}
			return ssh.ForwardNotDispatched, fmt.Errorf("owned test cancellation withheld before dispatch")
		}
		return ssh.CancelForward(ctx, connection, local, remote, logger)
	}
}

// TestProbeFailure changes only readiness observation, never the OPEN reply.
func (r *Reconciler) TestProbeFailure(port int, armed *atomic.Bool) {
	r.probe = func(ctx context.Context, local int) error {
		if local == port && armed.Load() {
			return fmt.Errorf("owned test readiness observation withheld")
		}
		return util.ProbePort(ctx, local)
	}
}
