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
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

func TestRemoteDockerPing(t *testing.T) {
	for _, tc := range []struct {
		name, host, destination string
		port                    []string
	}{
		{"default", "ssh://app@fixture.invalid", "app@fixture.invalid", nil},
		{"port", "ssh://app@fixture.invalid:2222", "app@fixture.invalid", []string{"-p", "2222"}},
		{"option", "ssh://-oProxyCommand=false", "-oProxyCommand=false", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("PATH", dir)
			t.Setenv("PING_DIR", dir)
			t.Setenv("DOCKER_HOST", "ssh://unrelated.invalid:1")
			t.Setenv("DOCKER_CONTEXT", "unrelated-context")
			t.Setenv("DOCKER_TLS_VERIFY", "1")
			t.Setenv("DOCKER_CERT_PATH", "/unrelated-certificates")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$PING_DIR/ssh-args\"\nexport PING_REMOTE=yes\nfor last do :; done\nexec /bin/sh -c \"$last\"\n"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\nif [ \"${PING_REMOTE:-}\" != yes ]; then printf local > \"$PING_DIR/local-docker\"; fi\nprintf '%s\\000' \"$@\" > \"$PING_DIR/docker-args\"\nprintf works\nexit \"${PING_EXIT:-0}\"\n"), 0700))
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			master, err := ssh.NewMaster(tc.host, logger)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, master.Close()) })
			m := NewManager(&config.Config{Host: tc.host}, nil, nil, master, nil, nil, logger)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var previous string
			for range 2 {
				require.NoError(t, m.watchdog.dockerPing.Ping(ctx))
				require.NoFileExists(t, filepath.Join(dir, "local-docker"), "ping used ambient Docker instead of application SSH target")
				data, err := os.ReadFile(filepath.Join(dir, "ssh-args"))
				require.NoError(t, err)
				args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
				want := append([]string{"-o", "BatchMode=yes", "-S", master.ControlPath()}, tc.port...)
				want = append(want, "--", tc.destination)
				require.Equal(t, want, args[:len(args)-1])
				data, err = os.ReadFile(filepath.Join(dir, "docker-args"))
				require.NoError(t, err)
				operands := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
				require.Len(t, operands, 9)
				name := operands[3]
				require.Regexp(t, `^rdhpf-ping-[0-9]+$`, name)
				require.NotEqual(t, previous, name)
				previous = name
				require.Equal(t, []string{"run", "--rm", "--name", name, "--label", "rdhpf.health-check=true", "alpine:latest", "echo", "works"}, operands)
			}
			t.Setenv("PING_EXIT", "17")
			require.ErrorContains(t, m.watchdog.dockerPing.Ping(ctx), "event health ping docker run failed: exit status 17")
			cancel()
			require.ErrorIs(t, m.watchdog.dockerPing.Ping(ctx), context.Canceled, "cancellation must not be mistaken for the prior exit-status failure")
		})
	}
}
