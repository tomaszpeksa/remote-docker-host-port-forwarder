//go:build rdhpf_integration

package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func forwardingPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return port
}
func forwardingHTTP(h *discoveryOwned, b discoveryBackend) error {
	response, err := h.client.Get(fmt.Sprintf("http://127.0.0.1:%d/", b.port))
	if err != nil {
		return err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
	closeErr := response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if response.StatusCode != http.StatusOK || string(body) != b.body {
		return fmt.Errorf("HTTP token mismatch status=%d body=%q", response.StatusCode, body)
	}
	h.t.Logf("S12_HTTP owner=%s port=%d status=%d body=%q", b.id, b.port, response.StatusCode, body)
	return nil
}
func forwardingRetirement(t *testing.T, h *discoveryOwned) {
	t.Helper()
	receipt := h.m.sshMaster.TestWaitReceipt()
	t.Cleanup(func() {
		h.close()
		pid, waitErr, processState := receipt()
		require.NotNil(t, processState)
		var status syscall.WaitStatus
		_, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		require.ErrorIs(t, err, syscall.ECHILD)
		t.Logf("S12_ORIGINAL_MASTER_WAIT pid=%d error=%v state=%v ECHILD=%v", pid, waitErr, processState, err)
	})
}

// A setup-only health probe supplies real accepted Engine events. It is not a
// desired-port repair trigger and is never used after the measured release.
func forwardingSubscribed(h *discoveryOwned) {
	h.t.Helper()
	end := time.Now().Add(8 * time.Second)
	for time.Now().Before(end) {
		require.NoError(h.t, h.m.watchdog.dockerPing.Ping(h.ctx))
		select {
		case event := <-h.events:
			h.t.Logf("S12_SETUP_EVENT full-ID=%s type=%s", event.ContainerID, event.Type)
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	h.t.Fatal("no real setup event accepted")
}

func TestManagerForwardingOwnedCancellation(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		pending, restart bool
	}{{name: "FailedCancel"}, {name: "PendingOwnedStop", pending: true}, {name: "RestartDuringClose", restart: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			pending := scenario.pending
			h := newDiscoveryOwned(t)
			forwardingRetirement(t, h)
			t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "1")
			port := forwardingPort(t)
			backend := h.f.Backend(sshfixture.Nginx, port, "rdhpf.test-infrastructure=true")
			t.Cleanup(h.close)
			body := "forwarding-" + h.f.Alias + "-target"
			h.f.Docker("exec", backend, "sh", "-c", "printf %s "+body+" > /usr/share/nginx/html/index.html")
			marker := h.f.Backend(sshfixture.Alpine, 0, fmt.Sprintf("rdhpf.forward.%d=ignored", port))
			t.Cleanup(h.close)
			target := discoveryBackend{id: marker, body: body, port: port}
			sibling := h.backend()
			var cancelFault, probeFault atomic.Bool
			probeFault.Store(pending)
			attempts := make(chan struct{}, 1)
			h.m.reconciler.TestCancelFailure(port, &cancelFault, attempts)
			h.m.reconciler.TestProbeFailure(port, &probeFault)
			var freezeEvents atomic.Bool
			if scenario.restart {
				realStream := h.m.discovery.openStream
				h.m.discovery.openStream = func(ctx context.Context, deliver func(context.Context, docker.Event) error) <-chan docker.StreamResult {
					return realStream(ctx, func(ctx context.Context, event docker.Event) error {
						if freezeEvents.Load() {
							return nil
						}
						return deliver(ctx, event)
					})
				}
			}
			realList := h.m.discovery.list
			var lists atomic.Int64
			h.m.discovery.list = func(ctx context.Context) ([]string, error) {
				if lists.Add(1) == 1 {
					return realList(ctx)
				}
				return nil, fmt.Errorf("owned test inventory withheld after initial observation")
			}
			h.start()
			h.wait("S12_TARGET_AND_SIBLING_HTTP", func() error {
				if err := forwardingHTTP(h, target); err != nil {
					return err
				}
				return h.active(sibling)
			})
			h.wait("S12_OWNED_STATUS", func() error {
				actual := h.m.state.GetByContainer(marker)
				wanted := "active"
				if pending {
					wanted = "pending"
				}
				if len(actual) != 1 || actual[0].Status != wanted {
					return fmt.Errorf("status=%v wanted=%s", actual, wanted)
				}
				return nil
			})
			created := h.m.state.GetByContainer(marker)[0].CreatedAt
			resource, ok := h.m.state.Resource(marker, port)
			require.True(t, ok)
			require.Equal(t, state.ForwardOwned, resource.Ownership)
			forwardingSubscribed(h)
			cancelFault.Store(true)
			h.stop(target) // The independently owned HTTP backend deliberately survives.
			select {
			case <-attempts:
			case <-time.After(8 * time.Second):
				t.Fatal("S12_CANCEL_FAULT_NOT_REACHED")
			}
			h.wait("S12_FAILED_CANCEL_RETAINED", func() error {
				resource, ok := h.m.state.Resource(marker, port)
				if !ok || !resource.Remove || resource.Ownership != state.ForwardOwned {
					return fmt.Errorf("resource=%+v present=%t", resource, ok)
				}
				return forwardingHTTP(h, target)
			})
			require.Empty(t, h.m.history.GetAll(), "S12_NO_HISTORY_BEFORE_CANCEL_ACK")
			require.NoError(t, h.active(sibling))
			if scenario.restart {
				h.verify(marker)
				h.f.Docker("start", marker)
				h.wait("S12_RESTART_DESIRE_ACKNOWLEDGED_DURING_CLOSE", func() error {
					for _, owner := range h.m.state.GetDesired() {
						if owner.ContainerID == marker && len(owner.Ports) == 1 && owner.Ports[0] == port && !h.m.hasUnapplied() {
							return nil
						}
					}
					return fmt.Errorf("restart not yet handled and acknowledged")
				})
				freezeEvents.Store(true)
				h.wait("S12_RESTART_ACCEPTED_EVENTS_DRAINED", func() error {
					h.m.discovery.mu.Lock()
					defer h.m.discovery.mu.Unlock()
					if h.m.discovery.pending != 0 || h.m.discovery.request != h.m.discovery.attempted {
						return fmt.Errorf("accepted restart still pending")
					}
					return nil
				})
				target.body += "-restarted"
				h.f.Docker("exec", backend, "sh", "-c", "printf %s "+target.body+" > /usr/share/nginx/html/index.html")
				require.NoError(t, forwardingHTTP(h, target), "old listener still owned before CLOSE acknowledgment")
			}
			// The only release is at the exact helper call seam. No delivered event,
			// successful inventory, desired mutation or manual Apply follows.
			cancelFault.Store(false)
			if scenario.restart {
				h.wait("S12_RESTART_CLOSE_ACK_BEFORE_REOPEN", func() error {
					resource, ok := h.m.state.Resource(marker, port)
					if !ok || resource.Remove || resource.Ownership != state.ForwardUnowned || len(h.m.history.GetAll()) != 1 {
						return fmt.Errorf("old CLOSE not yet acknowledged: %+v", resource)
					}
					if forwardingHTTP(h, target) == nil {
						return fmt.Errorf("old listener still serves HTTP after CLOSE")
					}
					return h.active(sibling)
				})
				h.wait("S12_RESTART_DURING_CLOSE_EVENTLESS_HTTP", func() error {
					if err := h.active(target); err != nil {
						return err
					}
					return h.active(sibling)
				})
				require.NoError(t, forwardingHTTP(h, target))
				require.False(t, h.m.hasUnapplied())
				t.Logf("S12_RESTART_CLOSE_PROOF full-ID=%s old-CLOSE-ack-before-new-OPEN=true delivered-events-after-release=0 list_calls=%d history=1", marker, lists.Load())
			} else {
				h.wait("S12_CANCEL_RETRY_WITHOUT_EVENT", func() error {
					if err := h.absent(target); err != nil {
						return err
					}
					return h.active(sibling)
				})
			}
			h.history(target, created)
			t.Logf("S12_CANCEL_PROOF pending=%t marker=%s backend=%s generation=%d tuple=127.0.0.1:%d:localhost:%d sibling=%s", pending, marker, backend, resource.Generation, port, port, sibling.id)
			h.close()
		})
	}
}

func TestManagerForwardingOwnedConflict(t *testing.T) {
	h := newDiscoveryOwned(t)
	forwardingRetirement(t, h)
	target, sibling := h.backend(), h.backend()
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", target.port))
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	realList := h.m.discovery.list
	var lists atomic.Int64
	h.m.discovery.list = func(ctx context.Context) ([]string, error) {
		if lists.Add(1) == 1 {
			return realList(ctx)
		}
		return nil, fmt.Errorf("owned test inventory withheld")
	}
	var admitted atomic.Int64
	h.m.discovery.openStream = func(ctx context.Context, _ func(context.Context, docker.Event) error) <-chan docker.StreamResult {
		return h.rawStream(ctx, func(context.Context, docker.Event) error { admitted.Add(1); return nil })
	}
	// Exclude the startup generation wake, exactly as in the sensitive native test.
	select {
	case <-h.m.sshMaster.Changes():
	default:
		t.Fatal("initial Master notification missing")
	}
	h.start()
	h.wait("S12_INITIAL_CONFLICT", func() error {
		actual := h.m.state.GetByContainer(target.id)
		if len(actual) != 1 || actual[0].Status != "conflict" || h.m.hasUnapplied() {
			return fmt.Errorf("initial attempt incomplete: %v", actual)
		}
		return h.active(sibling)
	})
	created := h.m.state.GetByContainer(target.id)[0].CreatedAt
	// Keep the owned conflict longer than the original complete immediate burst.
	time.Sleep(1600 * time.Millisecond)
	require.NoError(t, h.active(sibling))
	require.NoError(t, listener.Close())
	released := time.Now()
	h.wait("S12_RELEASED_CONFLICT_HTTP_NO_EVENT", func() error {
		if err := h.active(target); err != nil {
			return err
		}
		return h.active(sibling)
	})
	require.Equal(t, created, h.m.state.GetByContainer(target.id)[0].CreatedAt)
	require.Empty(t, h.m.history.GetAll())
	require.False(t, h.m.hasUnapplied())
	t.Logf("S12_CONFLICT_RELEASE target=%s port=%d released=%s restored=%s real_records_read_not_delivered=%d list_calls=%d", target.id, target.port, released, time.Now(), admitted.Load(), lists.Load())
	h.close()
}

func TestManagerForwardingOwnedConfiguredCollateral(t *testing.T) {
	h := newDiscoveryOwned(t)
	target, sibling := h.backend(), h.backend()
	local, dynamic, remote := forwardingPort(t), forwardingPort(t), forwardingPort(t)
	accountHome, err := os.UserHomeDir()
	require.NoError(t, err)
	path := filepath.Join(accountHome, ".ssh", "config")
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	localSocket := filepath.Join(h.f.Dir, "configured-local.sock")
	remoteSocket := "/tmp/" + h.f.Alias + "-remote.sock"
	declarations := fmt.Sprintf("  LocalForward 127.0.0.1:%d localhost:%d\n  LocalForward 127.0.0.1:%d localhost:%d\n  RemoteForward 127.0.0.1:%d localhost:%d\n  DynamicForward 127.0.0.1:%d\n  LocalForward %s /tmp/rdhpf-unused-target.sock\n  RemoteForward %s /tmp/rdhpf-unused-client.sock\n  Tunnel yes\n", target.port, target.port, local, sibling.port, remote, sibling.port, dynamic, localSocket, remoteSocket)
	header := "Host " + h.f.Alias + "\n"
	require.True(t, strings.HasPrefix(string(saved), header))
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(saved), header, header+declarations, 1)), info.Mode().Perm()))
	t.Cleanup(func() { h.close(); require.NoError(t, os.WriteFile(path, saved, info.Mode().Perm())) })
	// No app forwards exist yet; this separately exercises Master startup policy.
	require.NoError(t, h.m.sshMaster.Close())
	require.NoError(t, h.m.sshMaster.Open(h.ctx))
	forwardingRetirement(t, h)
	absent := func(port int) {
		conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		require.True(t, errors.Is(err, syscall.ECONNREFUSED), "configured local listener unexpectedly exists: %d %v", port, err)
	}
	assertSuppressed := func() {
		absent(local)
		absent(dynamic)
		require.NoFileExists(t, localSocket)
		sockets := h.f.Docker("exec", h.f.Container, "cat", "/proc/net/tcp")
		for _, line := range strings.Split(sockets, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 3 {
				require.False(t, fields[1] == fmt.Sprintf("0100007F:%04X", remote) && fields[3] == "0A", "configured remote TCP listener exists")
			}
		}
		h.f.Docker("exec", h.f.Container, "test", "!", "-S", remoteSocket)
	}
	absent(target.port)
	assertSuppressed()
	t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "1")
	marker := h.f.Backend(sshfixture.Alpine, 0, "rdhpf.test-infrastructure=true", fmt.Sprintf("rdhpf.forward.%d=ignored", local))
	t.Cleanup(h.close)
	ports, err := docker.InspectPorts(h.ctx, h.f.Host, h.m.sshMaster.ControlPath(), marker)
	require.NoError(t, err)
	require.Equal(t, []int{local}, ports)
	_, err = h.m.listRunning(h.ctx)
	require.NoError(t, err)
	_, err = h.m.hasTestInfrastructureLabel(h.ctx, h.m.sshMaster.ControlPath(), marker)
	require.NoError(t, err)
	require.NoError(t, h.m.validateDockerConnectivity(h.ctx, h.m.sshMaster.ControlPath()))
	require.NoError(t, h.m.watchdog.dockerPing.Ping(h.ctx))
	// Ordinary native fallback is permitted for these read-only commands, but
	// still must not establish configured forwarding or Tunnel devices.
	missing := filepath.Join(h.f.Dir, "missing-mux")
	ports, err = docker.InspectPorts(h.ctx, h.f.Host, missing, marker)
	require.NoError(t, err)
	require.Equal(t, []int{local}, ports)
	_, err = h.m.hasTestInfrastructureLabel(h.ctx, missing, marker)
	require.NoError(t, err)
	require.NoError(t, h.m.validateDockerConnectivity(h.ctx, missing))
	require.NoError(t, newRemoteDockerPingRunner(h.f.Host, missing, h.m.logger).Ping(h.ctx))
	assertSuppressed()
	absent(target.port)
	h.start()
	h.wait("S12_CONFIGURED_APP_HTTP", func() error {
		if err := h.active(target); err != nil {
			return err
		}
		return h.active(sibling)
	})
	forwardingSubscribed(h)
	assertSuppressed()
	h.stop(target)
	h.wait("S12_EXACT_CLOSE_PRESERVES_SIBLING", func() error {
		if err := h.absent(target); err != nil {
			return err
		}
		return h.active(sibling)
	})
	assertSuppressed()
	h.f.NoPrompt()
	t.Logf("S12_CONFIG_COLLATERAL_SUPPRESSED alias=%s same_tuple=%d unrelated=%d dynamic=%d remote=%d streamlocal=true Tunnel=no; real startup/inspect/labels/list/infrastructure/connectivity/ping/events, sibling HTTP preserved", h.f.Alias, target.port, local, dynamic, remote)
	h.close()
}
