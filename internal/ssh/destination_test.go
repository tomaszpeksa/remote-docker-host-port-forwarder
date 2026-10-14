package ssh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Removing the destination separator must fail even for an option-looking host.
// Only the executable is replaced; Master owns and checks a real fixture child.
func TestMasterDestinationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, host, destination string
		port                    []string
	}{
		{"default", "ssh://docker@fixture.invalid", "docker@fixture.invalid", nil},
		{"port", "ssh://user@fixture.invalid:2222", "user@fixture.invalid", []string{"-p", "2222"}},
		{"ipv6", "ssh://user@[::1]:2222", "user@[::1]", []string{"-p", "2222"}},
		{"option", "ssh://-oProxyCommand=false", "-oProxyCommand=false", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, dir := masterFixture(t)
			m.host = tc.host
			t.Setenv("SSH_TEST_KEY_PATH", "/identity with spaces")
			t.Setenv("S07_CHECK_MARKER", filepath.Join(dir, "check-args"))
			require.NoError(t, m.Open(context.Background()))
			data, err := os.ReadFile(filepath.Join(dir, "args"))
			require.NoError(t, err)
			want := []string{"-MN", "-o", "ControlMaster=yes", "-o", "ForkAfterAuthentication=no", "-o", "ControlPersist=no",
				"-o", "ControlPath=" + m.ControlPath(), "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
				"-o", "TCPKeepAlive=yes", "-o", "ExitOnForwardFailure=yes", "-o", "BatchMode=yes",
				"-i", "/identity with spaces"}
			want = append(want, tc.port...)
			want = append(want, "--", tc.destination)
			assert.Equal(t, want, strings.Split(string(data), "\n"))
			require.NoError(t, m.Check())
			data, err = os.ReadFile(filepath.Join(dir, "check-args"))
			require.NoError(t, err)
			want = append([]string{"-o", "BatchMode=yes", "-S", m.ControlPath(), "-O", "check"}, tc.port...)
			want = append(want, "--", tc.destination)
			require.Equal(t, want, strings.Split(string(data), "\x00"))
		})
	}
}

func TestForwardDestinationBoundary(t *testing.T) {
	for _, operation := range []string{"forward", "cancel"} {
		for _, tc := range []struct {
			name, host, destination string
			port                    []string
		}{
			{"default", "ssh://docker@fixture.invalid", "docker@fixture.invalid", nil},
			{"port", "ssh://user@fixture.invalid:2222", "user@fixture.invalid", []string{"-p", "2222"}},
			{"ipv6", "ssh://user@[::1]:2222", "user@[::1]", []string{"-p", "2222"}},
			{"option", "ssh://-oProxyCommand=false", "-oProxyCommand=false", nil},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				fake := installFakeSSH(t, sshResponse{})
				call := AddForward
				if operation == "cancel" {
					call = CancelForward
				}
				require.NoError(t, call(forwardTestContext(t), "/control socket", tc.host, 15432, 5432, quietForwardLogger()))
				want := append([]string{"-o", "BatchMode=yes", "-S", "/control socket"}, tc.port...)
				want = append(want, "-O", operation, "-L", "127.0.0.1:15432:localhost:5432", "--", tc.destination)
				require.Equal(t, [][]string{want}, fake.calls(t))
			})
		}
	}
}

// -G exits after parsing; -F disables both user and system configuration. The
// real production argv must not turn an option-looking destination into policy.
func TestMasterDestinationOpenSSHParser(t *testing.T) {
	executable, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is required for the nonconnecting parser proof")
	}
	m, dir := masterFixture(t)
	m.host = "ssh://-oProxyCommand=false"
	require.NoError(t, m.Open(context.Background()))
	data, err := os.ReadFile(filepath.Join(dir, "args"))
	require.NoError(t, err)
	require.NoError(t, m.Close())
	args := strings.Split(string(data), "\n")
	t.Logf("production argv: %q", args)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The additional harmless hostname is a control: without --, OpenSSH
	// consumes the option-looking destination as an option and uses this host.
	parserArgs := append([]string{"-G", "-F", "/dev/null", "-o", "CanonicalizeHostname=no"}, args...)
	parserArgs = append(parserArgs, "fixture.invalid")
	cmd := exec.CommandContext(ctx, executable, parserArgs...)
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "option-looking destination was accepted as SSH policy: %s", output)
	require.Contains(t, string(output), "hostname contains invalid characters")
	t.Logf("protected parser output: %s", output)
	var unprotected []string
	for _, arg := range parserArgs {
		if arg != "--" {
			unprotected = append(unprotected, arg)
		}
	}
	cmd = exec.CommandContext(ctx, executable, unprotected...)
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	output, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), "hostname fixture.invalid\n")
	require.Contains(t, string(output), "proxycommand false\n")
	t.Log("unprotected control: hostname fixture.invalid; proxycommand false")
}
