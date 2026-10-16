package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

// Probe the owned backend, not the forwarded listener whose behavior is under test.
func waitForNginxBackend(ctx context.Context, host, containerID string) error {
	if _, bounded := ctx.Deadline(); !bounded {
		return fmt.Errorf("nginx readiness requires a deadline")
	}
	sshHost, port, err := ssh.ParseHost(host)
	if err != nil {
		return err
	}
	args := []string{"-o", "BatchMode=yes"}
	if key := os.Getenv("SSH_TEST_KEY_PATH"); key != "" {
		args = append(args, "-i", key)
	}
	if port != "" {
		args = append(args, "-p", port)
	}
	args = append(args, "--", sshHost, "docker", "exec", containerID, "wget", "-q", "-O", "/dev/null", "-T", "1", "http://127.0.0.1:80/")
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("nginx backend readiness: %w", err)
		}
		if err := exec.CommandContext(ctx, "ssh", args...).Run(); err == nil {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("nginx backend readiness: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func TestNginxBackendReadiness(t *testing.T) {
	t.Run("eventually ready with exact remote argv", func(t *testing.T) {
		root := t.TempDir()
		calls := filepath.Join(root, "calls")
		argv := filepath.Join(root, "argv")
		fakeIntegrationSSH(t, "printf '%s\\n' \"$@\" > '"+argv+"'\n"+
			"if [ ! -f '"+calls+"' ]; then printf 1 > '"+calls+"'; exit 1; fi\nprintf 2 > '"+calls+"'\nexit 0")
		t.Setenv("SSH_TEST_KEY_PATH", "/fixture/key")
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, waitForNginxBackend(ctx, "ssh://user@fixture:2222", "owned-nginx"))
		count, err := os.ReadFile(calls)
		require.NoError(t, err)
		require.Equal(t, "2", string(count))
		got, err := os.ReadFile(argv)
		require.NoError(t, err)
		require.Equal(t, "-o\nBatchMode=yes\n-i\n/fixture/key\n-p\n2222\n--\nuser@fixture\ndocker\nexec\nowned-nginx\nwget\n-q\n-O\n/dev/null\n-T\n1\nhttp://127.0.0.1:80/\n", string(got))
	})
	for _, tc := range []struct{ name, script string }{
		{"unavailable", "exit 1"},
		{"hung command", "exec /bin/sleep 30"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeIntegrationSSH(t, tc.script)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			require.ErrorIs(t, waitForNginxBackend(ctx, "ssh://user@fixture:2222", "owned-nginx"), context.DeadlineExceeded)
		})
	}
	t.Run("deadline required", func(t *testing.T) {
		fakeIntegrationSSH(t, "exit 99")
		require.ErrorContains(t, waitForNginxBackend(context.Background(), "ssh://fixture", "owned-nginx"), "requires a deadline")
	})
}
