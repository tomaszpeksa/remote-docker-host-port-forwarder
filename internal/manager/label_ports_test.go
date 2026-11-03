package manager

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/config"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/docker"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

func TestManagerExplicitLabelPorts(t *testing.T) {
	for _, consumer := range []string{"start", "inventory"} {
		for _, enabled := range []bool{false, true} {
			t.Run(consumer+map[bool]string{false: "/off", true: "/on"}[enabled], func(t *testing.T) {
				dir, err := os.MkdirTemp("/tmp", "s17-labels-")
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
				t.Setenv("HOME", dir)
				t.Setenv("PATH", dir)
				t.Setenv("LABEL_CALLS", filepath.Join(dir, "calls"))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte(`#!/bin/sh
case "$*" in
 *rdhpf.test-infrastructure*) printf 'false\n';;
 *HostConfig.PortBindings*) printf '{}\n';;
 *Config.Labels*) printf label >> "$LABEL_CALLS"; printf '{"rdhpf.forward.8080":"80"}\n';;
 *) exit 91;;
esac
`), 0700))
				cfg := &config.Config{Host: "ssh://fixture.invalid", EnableLabelPorts: enabled}
				// Capture the programmatic choice, then change the ambient input.
				t.Setenv("RDHPF_ENABLE_LABEL_PORTS", map[bool]string{true: "0", false: "1"}[enabled])
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				master, err := ssh.NewMaster(cfg.Host, logger)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, master.Close()) })
				st := state.NewState()
				m := NewManager(cfg, nil, nil, master, st, state.NewHistory(), logger)
				var ports []int
				if consumer == "start" {
					require.NoError(t, m.handleStartEvent(context.Background(), docker.Event{Type: "start", ContainerID: identityContainerID}, 0))
					desired := st.GetDesired()
					if enabled {
						require.Len(t, desired, 1)
						ports = desired[0].Ports
					} else {
						require.Empty(t, desired)
					}
				} else {
					ports, err = m.observeContainer(context.Background(), identityContainerID)
					require.NoError(t, err)
				}
				if enabled {
					require.Equal(t, []int{8080}, ports)
					calls, err := os.ReadFile(filepath.Join(dir, "calls"))
					require.NoError(t, err)
					require.Equal(t, "label", string(calls))
				} else {
					require.Empty(t, ports)
					_, err := os.Stat(filepath.Join(dir, "calls"))
					require.True(t, os.IsNotExist(err))
				}
			})
		}
	}
}
