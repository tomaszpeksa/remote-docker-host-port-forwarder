package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// installFakeSSH isolates command tests from the user's SSH executable and config.
// The fixture uses only shell builtins; captured arguments are never evaluated.
// Callers must stay serial because PATH and HOME are process-wide.
func installFakeSSH(t *testing.T, stdout, stderr string, status int) func() []string {
	t.Helper()
	dir := t.TempDir()
	argvPath := filepath.Join(dir, "argv")
	sshPath := filepath.Join(dir, "ssh")
	require.NoError(t, os.WriteFile(sshPath, []byte(`#!/bin/sh
set -eu
printf '%s\000' "$@" >> "$FAKE_SSH_ARGV"
printf '%s' "$FAKE_SSH_STDOUT"
printf '%s' "$FAKE_SSH_STDERR" >&2
exit "$FAKE_SSH_STATUS"
`), 0700))
	t.Setenv("PATH", dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RDHPF_ENABLE_LABEL_PORTS", "0")
	t.Setenv("FAKE_SSH_ARGV", argvPath)
	t.Setenv("FAKE_SSH_STDOUT", stdout)
	t.Setenv("FAKE_SSH_STDERR", stderr)
	t.Setenv("FAKE_SSH_STATUS", strconv.Itoa(status))
	resolved, err := exec.LookPath("ssh")
	require.NoError(t, err)
	require.Equal(t, sshPath, resolved)

	return func() []string {
		t.Helper()
		data, err := os.ReadFile(argvPath)
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(string(data), "\x00"), "unterminated argv: %q", data)
		return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	}
}
