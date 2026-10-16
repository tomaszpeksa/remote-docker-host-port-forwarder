package manager

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

const identityContainerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Only Docker's verified ps formatter boundary is modeled. Manager startup,
// event handlers, State and Reconciler.Diff/Apply all execute unchanged. The
// owned TCP listener satisfies ProbePort; it is not remote-traffic evidence.
func TestManagerPreexistingContainerIdentity(t *testing.T) {
	for _, scenario := range []string{"startup", "die", "stop", "repeated-stop-die", "restart"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("PATH", dir)
			t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "")
			calls := filepath.Join(dir, "calls")
			t.Setenv("IDENTITY_CALLS", calls)
			t.Setenv("IDENTITY_FULL", identityContainerID)
			t.Setenv("IDENTITY_SHORT", identityContainerID[:12])
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, listener.Close()) })
			port := listener.Addr().(*net.TCPAddr).Port
			t.Setenv("IDENTITY_PORT", strconv.Itoa(port))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(`#!/bin/sh
set -eu
printf '%s\000' "$@" >> "$IDENTITY_CALLS"
printf '\000' >> "$IDENTITY_CALLS"
for arg do
 case "$arg" in
  *"docker ps "*)
   case "$arg" in *"--no-trunc"*) printf '%s\n' "$IDENTITY_FULL";; *) printf '%s\n' "$IDENTITY_SHORT";; esac
   exit 0;;
  *"rdhpf.test-infrastructure"*) printf 'false\n'; exit 0;;
  *"HostConfig.PortBindings"*) printf '{"80/tcp":[{"HostPort":"%s"}]}\n' "$IDENTITY_PORT"; exit 0;;
  forward|cancel) exit 0;;
 esac
done
printf 'unexpected identity fixture command\n' >&2
exit 91
`), 0700))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			master, err := ssh.NewMaster("ssh://owned-fixture.invalid:2222", logger)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, master.Close()) })
			st, history := state.NewState(), state.NewHistory()
			m := NewManager(&config.Config{Host: "ssh://owned-fixture.invalid:2222"}, nil, reconcile.NewReconciler(st, history, logger), master, st, history, logger)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			// Startup itself supplies all desired state. An unopened Master keeps
			// its existing nonfatal unavailable-connection behavior; exercise the
			// same private reconciliation operation with controlled command results.
			require.NoError(t, startupInventoryForTest(m, ctx))
			require.NoError(t, m.reconcileConnection(ctx, 0, true))
			require.Len(t, st.GetActual(), 1)
			if scenario != "startup" {
				kind := "stop"
				if scenario == "die" {
					kind = "die"
				}
				require.NoError(t, m.handleStopEvent(ctx, docker.Event{Type: kind, ContainerID: identityContainerID}, 0))
				require.NoError(t, m.reconcileConnection(ctx, 0, true))
				require.Empty(t, st.GetActual(), "full-ID stop must remove the startup owner")
				require.Equal(t, []state.ContainerPorts{{ContainerID: identityContainerID, Ports: []int{}}}, st.GetDesired())
				require.Len(t, history.GetAll(), 1)
				require.Equal(t, identityContainerID, history.GetAll()[0].ContainerID)
				require.Equal(t, "container stopped", history.GetAll()[0].EndReason)
				if scenario == "repeated-stop-die" {
					for _, repeated := range []string{"stop", "die", "stop"} {
						require.NoError(t, m.handleStopEvent(ctx, docker.Event{Type: repeated, ContainerID: identityContainerID}, 0))
						require.NoError(t, m.reconcileConnection(ctx, 0, true))
					}
					require.Empty(t, st.GetActual())
					require.Len(t, history.GetAll(), 1, "repeated lifecycle records must not remove another owner")
				}
				if scenario == "restart" {
					require.NoError(t, m.handleStartEvent(ctx, docker.Event{Type: "start", ContainerID: identityContainerID}, 0))
					require.NoError(t, m.reconcileConnection(ctx, 0, true))
					require.Len(t, history.GetAll(), 1)
				}
			}
			if scenario == "startup" || scenario == "restart" {
				require.Equal(t, []state.ContainerPorts{{ContainerID: identityContainerID, Ports: []int{port}}}, st.GetDesired(), "startup and events must share the full owner")
				require.Len(t, st.GetActual(), 1)
				require.Equal(t, identityContainerID, st.GetActual()[0].ContainerID)
				require.Equal(t, "active", st.GetActual()[0].Status)
				require.Equal(t, port, st.GetActual()[0].Port)
			}
			data, err := os.ReadFile(calls)
			require.NoError(t, err)
			var forwards, cancels, inspections int
			for _, record := range strings.Split(strings.TrimSuffix(string(data), "\x00\x00"), "\x00\x00") {
				args := strings.Split(record, "\x00")
				require.Equal(t, []string{"-o", "BatchMode=yes", "-S", master.ControlPath(), "-p", "2222"}, args[:6])
				switch args[6] {
				case "--":
					require.Equal(t, "owned-fixture.invalid", args[7])
					if strings.Contains(args[8], "docker inspect") {
						inspections++
						require.Contains(t, args[8], identityContainerID, "inspect must receive full inventory/event identity")
					}
				case "-O":
					require.Equal(t, []string{"-L", fmt.Sprintf("127.0.0.1:%d:localhost:%d", port, port), "--", "owned-fixture.invalid"}, args[8:])
					if args[7] == "forward" {
						forwards++
					} else {
						require.Equal(t, "cancel", args[7])
						cancels++
					}
				default:
					t.Fatalf("unexpected production argv: %q", args)
				}
			}
			wantForwards, wantCancels, wantInspections := 1, 1, 2
			if scenario == "startup" {
				wantCancels = 0
			}
			if scenario == "restart" {
				wantForwards, wantInspections = 2, 3
			}
			require.Equal(t, wantForwards, forwards)
			require.Equal(t, wantCancels, cancels)
			require.Equal(t, wantInspections, inspections)
			require.NoError(t, ctx.Err())
		})
	}
}
