package ssh

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type sshResponse struct {
	stdout, stderr string
	status         int
}

type fakeSSH struct {
	dir string
}

// installFakeSSH uses only shell builtins and never evaluates argv or invokes SSH.
// Environment-changing callers must remain serial. AddForward waits for each process.
func installFakeSSH(t *testing.T, responses ...sshResponse) *fakeSSH {
	t.Helper()
	fake := &fakeSSH{dir: t.TempDir()}
	var script strings.Builder
	script.WriteString(`#!/bin/sh
set -eu
count=0
if [ -f "$FAKE_SSH_DIR/count" ]; then
    IFS= read -r count < "$FAKE_SSH_DIR/count"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$FAKE_SSH_DIR/count"
printf '%s\000' "$@" > "$FAKE_SSH_DIR/argv-$count"
case "$count" in
`)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	for i, response := range responses {
		fmt.Fprintf(&script, "%d) printf '%%s' %s; printf '%%s' %s >&2; exit %d ;;\n",
			i+1, quote(response.stdout), quote(response.stderr), response.status)
	}
	script.WriteString("*) printf '%s' 'unexpected fake SSH invocation' >&2; exit 97 ;;\nesac\n")
	sshPath := filepath.Join(fake.dir, "ssh")
	require.NoError(t, os.WriteFile(sshPath, []byte(script.String()), 0700))
	t.Setenv("PATH", fake.dir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKE_SSH_DIR", fake.dir)
	resolved, err := exec.LookPath("ssh")
	require.NoError(t, err)
	require.Equal(t, sshPath, resolved)
	return fake
}

func (f *fakeSSH) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "count"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	var calls [][]string
	for i := 1; i <= count; i++ {
		data, err := os.ReadFile(filepath.Join(f.dir, fmt.Sprintf("argv-%d", i)))
		require.NoError(t, err)
		require.True(t, strings.HasSuffix(string(data), "\x00"), "unterminated argv: %q", data)
		calls = append(calls, strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"))
	}
	return calls
}
