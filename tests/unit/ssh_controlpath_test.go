package unit

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
)

func TestMasterControlPathPrivateAndStable(t *testing.T) {
	t.Setenv("HOME", filepath.Join(t.TempDir(), strings.Repeat("long", 60)))
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), strings.Repeat("long", 60)))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	seen := map[string]bool{}
	for _, host := range []string{"ssh://user@example.com", "ssh://user@example.com", "ssh://admin@example.com", "ssh://user@example.com:22", "ssh://user@example.com:2222"} {
		master, err := ssh.NewMaster(host, logger)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, master.Close()) })
		path := master.ControlPath()
		require.False(t, seen[path])
		seen[path] = true
		require.Equal(t, path, master.ControlPath())
		require.LessOrEqual(t, len(path)+18, 104)
		require.True(t, strings.HasPrefix(path, "/tmp/rdhpf-"))
		info, err := os.Stat(filepath.Dir(path))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	}
}

func TestMasterControlPathInvalidHost(t *testing.T) {
	for _, host := range []string{"user@example.com", "http://user@example.com", "ssh://", "ssh", ""} {
		t.Run(host, func(t *testing.T) {
			master, err := ssh.NewMaster(host, slog.Default())
			require.Error(t, err)
			require.Nil(t, master)
		})
	}
}

func TestMasterControlPathValidFormats(t *testing.T) {
	for _, host := range []string{"ssh://user@example.com", "ssh://user@example.com:2222", "ssh://user@192.168.1.100", "ssh://user@192.168.1.100:2222", "ssh://user@localhost", "ssh://user-name@example.com"} {
		t.Run(host, func(t *testing.T) {
			master, err := ssh.NewMaster(host, slog.Default())
			require.NoError(t, err)
			require.NotEmpty(t, master.ControlPath())
			require.NoError(t, master.Close())
		})
	}
}
