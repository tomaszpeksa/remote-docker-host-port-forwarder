package docker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDockerDestinationBoundary(t *testing.T) {
	for _, operation := range []struct {
		name, output, command string
	}{
		{"events", "", `sh -c "docker events --format '{{json .}}' --filter type=container --filter event=start --filter event=die --filter event=stop"`},
		{"ports", `{}`, `sh -c "docker inspect abc123def456 --format '{{json .HostConfig.PortBindings}}'"`},
		{"labels", `{"rdhpf.forward.8080":"80"}`, `sh -c "docker inspect abc123def456 --format '{{json .Config.Labels}}'"`},
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
				argv := installFakeSSH(t, operation.output, "", 0)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				switch operation.name {
				case "events":
					got, errs := collectStream(t, tc.host, "/control socket", slog.New(slog.NewTextHandler(io.Discard, nil)))
					require.Empty(t, got)
					require.Empty(t, errs)
				case "ports":
					got, err := InspectPorts(ctx, tc.host, "/control socket", "abc123def456")
					require.NoError(t, err)
					require.Empty(t, got)
				case "labels":
					got, err := inspectPortsFromLabels(ctx, tc.host, "/control socket", "abc123def456")
					require.NoError(t, err)
					require.Equal(t, []int{8080}, got)
				}
				want := append([]string{"-o", "BatchMode=yes", "-S", "/control socket"}, tc.port...)
				want = append(want, "--", tc.destination, operation.command)
				require.Equal(t, want, argv())
			})
		}
	}
}
