package docker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInspectPortsBindings(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         []int
		wantError    string
	}{
		{"published", `{"5432/tcp":[{"HostIp":"0.0.0.0","HostPort":"5432"}],"6379/tcp":[{"HostIp":"127.0.0.1","HostPort":"6379"}]}`, []int{5432, 6379}, ""},
		{"empty_host_port", `{"80/tcp":[{"HostIp":"","HostPort":""}]}`, []int{}, ""},
		{"valid_and_invalid_integer", `{"80/tcp":[{"HostPort":"invalid"},{"HostPort":"8080"},{"HostPort":""}]}`, []int{8080}, ""},
		{"duplicates_and_multiple_bindings", `{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"},{"HostIp":"::","HostPort":"8080"},{"HostPort":"8081"}],"81/tcp":[{"HostPort":"8080"},{"HostPort":"9090"}]}`, []int{8080, 8081, 9090}, ""},
		{"empty_object", `{}`, []int{}, ""},
		{"null", `null`, []int{}, ""},
		{"incomplete_json", `{"80/tcp":[`, nil, "failed to parse PortBindings JSON:"},
		{"not_json", `not JSON`, nil, "failed to parse PortBindings JSON:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := installFakeSSH(t, tc.output, "", 0)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := InspectPorts(ctx, "ssh://docker@fixture.invalid", "/control socket", eventContainerID)
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.NotNil(t, got)
				require.ElementsMatch(t, tc.want, got)
			}
			// Exactly one invocation: empty results must not consult ambient label settings.
			require.Len(t, argv(), 7)
		})
	}
}

func TestInspectPortsArgv(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		want       []string
	}{
		{"default_port", "ssh://docker@fixture.invalid", []string{"-o", "BatchMode=yes", "-S", "/control socket", "--", "docker@fixture.invalid"}},
		{"explicit_port", "ssh://user@fixture.invalid:2222", []string{"-o", "BatchMode=yes", "-S", "/control socket", "-p", "2222", "--", "user@fixture.invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := installFakeSSH(t, `{}`, "", 0)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := InspectPorts(ctx, tc.host, "/control socket", "abc123def456")
			require.NoError(t, err)
			require.Equal(t, []int{}, got)
			want := append(tc.want, `sh -c "docker inspect abc123def456 --format '{{json .HostConfig.PortBindings}}'"`)
			require.Equal(t, want, argv())
		})
	}
}

func TestInspectPortsCommandFailure(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, wantError string
	}{
		{"missing_object", "Error: No such object: abc123def456", "container not found: abc123def456"},
		{"missing_container", "Error: No such container: abc123def456", "container not found: abc123def456"},
		{"other_failure", "fixture failure", "docker inspect failed: fixture failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installFakeSSH(t, "", tc.stderr, 7)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := InspectPorts(ctx, "ssh://docker@fixture.invalid", "/control socket", "abc123def456")
			require.EqualError(t, err, tc.wantError)
			require.Nil(t, got)
		})
	}
}
