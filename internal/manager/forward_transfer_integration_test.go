//go:build rdhpf_integration

package manager

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// This controlled Reconciler journey uses real inspection, mux operations and
// HTTP, not Manager.Run or event-stream scheduling. Two unbound label markers
// share one independent backend; neither marker is itself the HTTP server.
func TestManagerForwardingOwnedClaimantTransfer(t *testing.T) {
	for _, failedAgain := range []bool{false, true} {
		name := "BeforeRetryAt"
		if failedAgain {
			name = "FailedAgain"
		}
		t.Run(name, func(t *testing.T) {
			h := newDiscoveryOwned(t)
			forwardingRetirement(t, h)
			t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "1")
			port := forwardingPort(t)
			backend := h.f.Backend(sshfixture.Nginx, port, "rdhpf.test-infrastructure=true")
			t.Cleanup(h.close)
			body := "transfer-" + h.f.Alias
			h.f.Docker("exec", backend, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
			marker := h.f.Backend(sshfixture.Alpine, 0, fmt.Sprintf("rdhpf.forward.%d=ignored", port))
			t.Cleanup(h.close)
			replacement := h.f.Backend(sshfixture.Alpine, 0, fmt.Sprintf("rdhpf.forward.%d=ignored", port))
			t.Cleanup(h.close)
			target := discoveryBackend{id: marker, body: body, port: port}
			next := discoveryBackend{id: replacement, body: body, port: port}
			sibling := h.backend()
			observe := func(b discoveryBackend) []int {
				h.verify(b.id)
				require.Equal(t, "true", h.f.Docker("inspect", "--format", "{{.State.Running}}", b.id))
				ports, err := docker.InspectPorts(h.ctx, h.f.Host, h.m.sshMaster.ControlPath(), b.id)
				require.NoError(t, err)
				require.Equal(t, []int{b.port}, ports)
				return ports
			}
			apply := func(retry bool) error {
				return h.m.sshMaster.WithForwarding(h.ctx, func(connection *ssh.Forwarding) error {
					h.m.reconciler.RetireBefore(connection.Generation())
					actions := h.m.reconciler.RetryActions()
					if !retry {
						add, remove := h.m.reconciler.Diff()
						actions = append(remove, add...)
					}
					return h.m.reconciler.Apply(h.ctx, connection, actions)
				})
			}
			waitRetry := func() {
				at, pending := h.m.reconciler.NextRetry()
				require.True(t, pending)
				timer := time.NewTimer(time.Until(at))
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-h.ctx.Done():
					t.Fatal(h.ctx.Err())
				}
			}
			var fault atomic.Bool
			attempts := make(chan struct{}, 8)
			h.m.reconciler.TestCancelFailure(port, &fault, attempts)
			h.m.state.SetDesired(marker, observe(target))
			h.m.state.SetDesired(sibling.id, observe(sibling))
			require.NoError(t, apply(false))
			h.wait("S12_TRANSFER_INITIAL_HTTP", func() error {
				if err := h.active(target); err != nil {
					return err
				}
				return h.active(sibling)
			})
			created := h.m.state.GetByContainer(marker)[0].CreatedAt
			siblingResource, ok := h.m.state.Resource(sibling.id, sibling.port)
			require.True(t, ok)
			h.stop(target)
			require.Equal(t, "false", h.f.Docker("inspect", "--format", "{{.State.Running}}", marker))
			h.m.state.SetDesired(marker, nil)
			fault.Store(true)
			require.Error(t, apply(false))
			require.Len(t, attempts, 1)
			require.NoError(t, forwardingHTTP(h, target), "old listener survives marker stop and failed CLOSE")
			h.f.Docker("start", marker)
			h.m.state.SetDesired(marker, observe(target))
			replacementPorts := observe(next)
			// Docker stop/start/inspection has no timing guarantee. A timer-only
			// failed cleanup establishes a fresh retry deadline before transfer.
			waitRetry()
			require.Error(t, apply(true))
			require.Len(t, attempts, 2)
			h.m.state.SetDesired(replacement, replacementPorts)
			if failedAgain {
				waitRetry()
			}
			require.Error(t, apply(false), "production transfer must wait for old ownership")
			wantedAttempts := 2
			if failedAgain {
				wantedAttempts++
			}
			require.Len(t, attempts, wantedAttempts, "deferred transfer must not dispatch CLOSE early")
			resource, ok := h.m.state.Resource(marker, port)
			require.True(t, ok)
			require.True(t, resource.Remove)
			require.True(t, resource.PreventRenewal)
			require.Equal(t, "container stopped", resource.EndReason)
			require.Equal(t, state.ForwardOwned, resource.Ownership)
			require.Empty(t, h.m.history.GetAll())
			require.NoError(t, h.active(sibling))
			target.body += "-after-transfer"
			next.body = target.body
			h.f.Docker("exec", backend, "sh", "-c", "printf %s "+target.body+" > /usr/share/nginx/html/index.html")
			require.NoError(t, forwardingHTTP(h, target))
			// From here only paced RetryActions may finish; no desired update,
			// event, inventory, or full Diff/Apply rescues the transfer.
			fault.Store(false)
			released := time.Now()
			for {
				waitRetry()
				require.NoError(t, apply(true))
				if len(h.m.history.GetAll()) != 0 {
					_, present := h.m.state.Resource(marker, port)
					require.False(t, present, "SUPERSEDING_CLAIMANT_MUST_NOT_RENEW_OLD_OWNER")
				}
				if _, pending := h.m.reconciler.NextRetry(); !pending {
					break
				}
				require.NoError(t, h.ctx.Err())
			}
			require.Empty(t, h.m.state.GetByContainer(marker), "SUPERSEDING_CLAIMANT_MUST_NOT_RENEW_OLD_OWNER")
			h.history(target, created)
			h.wait("S12_TRANSFER_TIMER_ONLY_HTTP", func() error {
				if err := h.active(next); err != nil {
					return err
				}
				return h.active(sibling)
			})
			require.NoError(t, forwardingHTTP(h, next))
			require.NoError(t, forwardingHTTP(h, sibling))
			currentSibling, ok := h.m.state.Resource(sibling.id, sibling.port)
			require.True(t, ok)
			require.Equal(t, siblingResource, currentSibling)
			require.Empty(t, h.m.reconciler.RetryActions())
			_, pending := h.m.reconciler.NextRetry()
			require.False(t, pending)
			t.Logf("S12_TRANSFER_PROOF variant=%s old_owner=%s new_owner=%s shared_backend=%s sibling=%s release=%s completed=%s history=1 postrelease_full_diff=0; controlled Reconciler scheduling, not Manager.Run", name, marker, replacement, backend, sibling.id, released, time.Now())
			h.close()
		})
	}
}
