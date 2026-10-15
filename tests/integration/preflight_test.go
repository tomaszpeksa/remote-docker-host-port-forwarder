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

func fakeIntegrationSSH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("PATH", dir)
	t.Setenv("SSH_TEST_HOST", "")
	t.Setenv("SSH_TEST_KEY_PATH", "")
	path := filepath.Join(dir, "ssh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700))
	return path
}

func TestIntegrationPreflightSentinel(t *testing.T) {
	mode := os.Getenv("PREFLIGHT_SENTINEL")
	if mode == "" {
		t.Skip("subprocess-only preflight sentinel")
	}
	t.Log("preflight sentinel reached")
	if mode == "fail" {
		t.Fatal("sentinel failure retained")
	}
}

func TestIntegrationPreflightEntrypoint(t *testing.T) {
	for _, tc := range []struct {
		name, host, script, sentinel   string
		missing, wantFailure, wantBody bool
	}{
		{name: "unrequested", script: "exit 99", sentinel: "pass", wantBody: true},
		{name: "missing SSH", host: "ssh://fixture:2222", missing: true, sentinel: "pass", wantFailure: true},
		{name: "unavailable Docker over SSH", host: "ssh://fixture:2222", script: "printf sensitive-fixture-output >&2; exit 23", sentinel: "pass", wantFailure: true},
		{name: "available", host: "ssh://fixture:2222", script: "exit 0", sentinel: "pass", wantBody: true},
		{name: "test failure", host: "ssh://fixture:2222", script: "exit 0", sentinel: "fail", wantFailure: true, wantBody: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "ssh-called")
			path := fakeIntegrationSSH(t, "printf called > '"+marker+"'\n"+tc.script)
			if tc.missing {
				require.NoError(t, os.Remove(path))
			}
			t.Setenv("SSH_TEST_HOST", tc.host)
			t.Setenv("PREFLIGHT_SENTINEL", tc.sentinel)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestIntegrationPreflightSentinel$")
			output, err := cmd.CombinedOutput()
			require.Equal(t, tc.wantFailure, err != nil, "output: %s", output)
			require.Equal(t, tc.wantBody, strings.Contains(string(output), "preflight sentinel reached"), "output: %s", output)
			require.NotContains(t, string(output), "sensitive-fixture-output")
			if !tc.wantBody {
				require.Contains(t, string(output), "requested integration preflight failed")
			}
			if tc.host == "" {
				_, err := os.Stat(marker)
				require.True(t, os.IsNotExist(err), "unrequested preflight must not invoke SSH")
			}
		})
	}
}
