package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGetSSHMasterPIDDestinationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, host, destination, port, key string
	}{
		{"alias", "configured-alias", "configured-alias", "", ""},
		{"user", "ssh://docker@configured-alias", "docker@configured-alias", "", ""},
		{"port", "ssh://user@fixture.invalid:2222", "user@fixture.invalid", "2222", "/identity with spaces"},
		{"ipv6", "ssh://user@[::1]:2222", "user@[::1]", "2222", ""},
		{"option", "ssh://-oProxyCommand=false", "-oProxyCommand=false", "", "/identity with spaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := fakeIntegrationSSH(t, `printf '%s\000' "$@" > "$ARGV_CAPTURE"
printf 'Master running (pid=12345)\n' >&2`)
			capture := filepath.Join(filepath.Dir(fake), "argv")
			t.Setenv("ARGV_CAPTURE", capture)
			t.Setenv("SSH_TEST_KEY_PATH", tc.key)
			control := filepath.Join(filepath.Dir(fake), "control path")
			require.Equal(t, 12345, getSSHMasterPID(t, tc.host, control))
			data, err := os.ReadFile(capture)
			require.NoError(t, err)
			want := []string{}
			if tc.key != "" {
				want = append(want, "-i", tc.key)
			}
			want = append(want, "-S", control, "-O", "check")
			if tc.port != "" {
				want = append(want, "-p", tc.port)
			}
			want = append(want, "--", tc.destination)
			require.Equal(t, want, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"))
		})
	}
}

func TestGetSSHMasterPIDFailures(t *testing.T) {
	for _, tc := range []struct {
		name, host, script string
	}{
		{"empty host", "", "exit 99"},
		{"invalid host", "ssh://user@[::1", "exit 99"},
		{"failed check", "configured-alias", "printf 'Master running (pid=12345)'; exit 1"},
		{"missing PID", "configured-alias", "printf 'not running'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeIntegrationSSH(t, tc.script)
			require.Equal(t, -1, getSSHMasterPID(t, tc.host, "/control path"))
		})
	}
}

func TestGetSSHMasterPIDOpenSSHParser(t *testing.T) {
	executable, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH is required for the nonconnecting parser proof")
	}
	fake := fakeIntegrationSSH(t, `printf '%s\000' "$@" > "$ARGV_CAPTURE"
printf 'Master running (pid=12345)\n'`)
	dir := filepath.Dir(fake)
	capture := filepath.Join(dir, "argv")
	t.Setenv("ARGV_CAPTURE", capture)
	require.Equal(t, 12345, getSSHMasterPID(t, "ssh://-oProxyCommand=false", filepath.Join(dir, "control path")))
	data, err := os.ReadFile(capture)
	require.NoError(t, err)
	args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	t.Logf("helper argv: %q", args)
	// -G only parses; -F prevents both user and system config loading. The
	// extra hostname exposes option interpretation if the separator is lost.
	parserArgs := append([]string{"-G", "-F", "/dev/null", "-o", "CanonicalizeHostname=no"}, args...)
	parserArgs = append(parserArgs, "fixture.invalid")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, parserArgs...)
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "%s", output)
	require.Contains(t, string(output), "hostname contains invalid characters")
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
	t.Log("protected hostname rejected; unprotected control sets proxycommand false")
}
