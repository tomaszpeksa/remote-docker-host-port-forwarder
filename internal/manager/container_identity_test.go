package manager

import (
	"context"
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
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/muxstub"
)

const identityContainerID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// Model only Docker's ps/inspect formatter boundary. Forwarding uses the real
// peer-verified helper against a retained owned stand-in; no remote traffic claim.
func TestManagerPreexistingContainerIdentity(t *testing.T) {
	for _, scenario := range []string{"startup", "die", "stop", "repeated-stop-die", "restart"} {
		t.Run(scenario, func(t *testing.T) {
			dir := muxstub.Install(t)
			original, err := os.ReadFile(filepath.Join(dir, "ssh"))
			require.NoError(t, err)
			calls := filepath.Join(dir, "docker-calls")
			t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "")
			t.Setenv("IDENTITY_CALLS", calls)
			t.Setenv("IDENTITY_FULL", identityContainerID)
			t.Setenv("IDENTITY_SHORT", identityContainerID[:12])
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, listener.Close()) })
			port := listener.Addr().(*net.TCPAddr).Port
			t.Setenv("IDENTITY_PORT", strconv.Itoa(port))
			native := "#!/bin/sh\ncase \"$*\" in '-MN '*|*'-O check'*)\n" + strings.TrimPrefix(string(original), "#!/bin/sh\n") + ";; esac\n"
			script := native + `set -eu
printf '%s\000' "$@" >> "$IDENTITY_CALLS"
printf '\000' >> "$IDENTITY_CALLS"
for arg do
 case "$arg" in
  *"docker ps "*)
   case "$arg" in *"--no-trunc"*) printf '%s\n' "$IDENTITY_FULL";; *) printf '%s\n' "$IDENTITY_SHORT";; esac
   exit 0;;
  *"rdhpf.test-infrastructure"*) printf 'false\n'; exit 0;;
  *"HostConfig.PortBindings"*) printf '{"80/tcp":[{"HostPort":"%s"}]}\n' "$IDENTITY_PORT"; exit 0;;
 esac
done
printf 'unexpected identity fixture command\n' >&2
exit 91
`
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0700))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			master, err := ssh.NewMaster("ssh://owned-fixture.invalid:2222", logger)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, master.Close()) })
			st, history := state.NewState(), state.NewHistory()
			m := NewManager(&config.Config{Host: "ssh://owned-fixture.invalid:2222"}, nil, reconcile.NewReconciler(st, history, logger), master, st, history, logger)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, master.Open(ctx))
			require.NoError(t, startupInventoryForTest(m, ctx))
			require.NoError(t, m.triggerReconcile(ctx))
			require.Len(t, st.GetActual(), 1)
			if scenario != "startup" {
				kind := "stop"
				if scenario == "die" {
					kind = "die"
				}
				require.NoError(t, m.handleStopEvent(ctx, docker.Event{Type: kind, ContainerID: identityContainerID}, 0))
				require.NoError(t, m.triggerReconcile(ctx))
				require.Empty(t, st.GetActual(), "full-ID stop must remove startup owner")
				require.Equal(t, []state.ContainerPorts{{ContainerID: identityContainerID, Ports: []int{}}}, st.GetDesired())
				require.Len(t, history.GetAll(), 1)
				require.Equal(t, identityContainerID, history.GetAll()[0].ContainerID)
				require.Equal(t, "container stopped", history.GetAll()[0].EndReason)
				if scenario == "repeated-stop-die" {
					for _, kind := range []string{"stop", "die", "stop"} {
						require.NoError(t, m.handleStopEvent(ctx, docker.Event{Type: kind, ContainerID: identityContainerID}, 0))
						require.NoError(t, m.triggerReconcile(ctx))
					}
					require.Empty(t, st.GetActual())
					require.Len(t, history.GetAll(), 1)
				}
				if scenario == "restart" {
					require.NoError(t, m.handleStartEvent(ctx, docker.Event{Type: "start", ContainerID: identityContainerID}, 0))
					require.NoError(t, m.triggerReconcile(ctx))
					require.Len(t, history.GetAll(), 1)
				}
			}
			if scenario == "startup" || scenario == "restart" {
				require.Equal(t, []state.ContainerPorts{{ContainerID: identityContainerID, Ports: []int{port}}}, st.GetDesired())
				require.Len(t, st.GetActual(), 1)
				require.Equal(t, identityContainerID, st.GetActual()[0].ContainerID)
				require.Equal(t, "active", st.GetActual()[0].Status)
				require.Equal(t, port, st.GetActual()[0].Port)
			}
			data, err := os.ReadFile(calls)
			require.NoError(t, err)
			inspections := 0
			for _, record := range strings.Split(strings.TrimSuffix(string(data), "\x00\x00"), "\x00\x00") {
				args := strings.Split(record, "\x00")
				require.Equal(t, []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", master.ControlPath(), "-p", "2222", "--", "owned-fixture.invalid"}, args[:12])
				if strings.Contains(args[12], "docker inspect") {
					inspections++
					require.Contains(t, args[12], identityContainerID)
				}
			}
			data, err = os.ReadFile(os.Getenv("S12_MUX_RECORD"))
			require.NoError(t, err)
			wantOpen, wantClose, wantInspect := 1, 1, 2
			if scenario == "startup" {
				wantClose = 0
			}
			if scenario == "restart" {
				wantOpen, wantInspect = 2, 3
			}
			require.Equal(t, wantOpen, strings.Count(string(data), "OPEN"))
			require.Equal(t, wantClose, strings.Count(string(data), "CLOSE"))
			require.Equal(t, wantInspect, inspections)
			require.NoError(t, ctx.Err())
		})
	}
}
