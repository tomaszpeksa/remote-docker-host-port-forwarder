package manager

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/reconcile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestManagerDestinationBoundary(t *testing.T) {
	for _, operation := range []struct {
		name, output, command string
	}{
		{"startup", "", `sh -c "docker ps --no-trunc --format '{{.ID}}'"`},
		{"label", "true", `sh -c 'docker inspect --format '"'"'{{index .Config.Labels "rdhpf.test-infrastructure"}}'"'"' -- '"'"'abc123def456'"'"''`},
		{"connectivity", "29.8.2", `sh -c "docker version --format '{{.Server.Version}}'"`},
	} {
		for _, tc := range []struct {
			name, host, destination string
			port                    []string
		}{
			{"default", "ssh://docker@fixture.invalid", "docker@fixture.invalid", nil},
			{"port", "ssh://user@fixture.invalid:2222", "user@fixture.invalid", []string{"-p", "2222"}},
			{"ipv6", "ssh://user@[::1]:2222", "user@[::1]", []string{"-p", "2222"}},
			{"option", "ssh://-oProxyCommand=false", "-oProxyCommand=false", nil},
		} {
			t.Run(operation.name+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("HOME", dir)
				t.Setenv("PATH", dir)
				t.Setenv("S07_MANAGER_ARGS", filepath.Join(dir, "args"))
				t.Setenv("S07_MANAGER_OUTPUT", operation.output)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nset -eu\nprintf '%s\\000' \"$@\" > \"$S07_MANAGER_ARGS\"\nprintf '%s' \"$S07_MANAGER_OUTPUT\"\n"), 0700))
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				master, err := ssh.NewMaster(tc.host, logger)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, master.Close()) })
				st, history := state.NewState(), state.NewHistory()
				m := NewManager(&config.Config{Host: tc.host}, nil, reconcile.NewReconciler(st, history, logger), master, st, history, logger)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				switch operation.name {
				case "startup":
					require.NoError(t, startupInventoryForTest(m, ctx))
				case "label":
					infrastructure, err := m.hasTestInfrastructureLabel(ctx, master.ControlPath(), "abc123def456")
					require.NoError(t, err)
					require.True(t, infrastructure)
				case "connectivity":
					require.NoError(t, m.validateDockerConnectivity(ctx, master.ControlPath()))
				}
				data, err := os.ReadFile(filepath.Join(dir, "args"))
				require.NoError(t, err)
				want := append([]string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", master.ControlPath()}, tc.port...)
				want = append(want, "--", tc.destination, operation.command)
				require.Equal(t, want, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"))
			})
		}
	}
}
