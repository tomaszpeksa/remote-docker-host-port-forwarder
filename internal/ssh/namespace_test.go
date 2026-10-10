package ssh

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMasterPrivateNamespaceLifecycle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := NewMaster("ssh://owned.invalid", logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, a.Close()) })
	b, err := NewMaster("ssh://owned.invalid", logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	require.NotEqual(t, a.ControlPath(), b.ControlPath())
	dir := filepath.Dir(a.ControlPath())
	info, err := os.Lstat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	require.LessOrEqual(t, len(a.ControlPath())+17+1, 104)
	require.NoError(t, a.Close(), "never-opened Master releases its directory")
	_, err = os.Lstat(dir)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, a.Close())
	require.NoError(t, os.Mkdir(dir, 0700))
	t.Cleanup(func() { require.NoError(t, os.Remove(dir)) })
	require.Error(t, a.Open(context.Background()), "occupied exclusive recreation must fail before SSH")
	require.NoError(t, a.Close(), "failed recreation acquired no cleanup authority")
	_, err = os.Lstat(dir)
	require.NoError(t, err, "Close must preserve occupant")
}

func TestMasterRetiresEarlySocketArtifacts(t *testing.T) {
	for _, both := range []bool{false, true} {
		t.Run(map[bool]string{false: "temporary-only", true: "temporary-and-final"}[both], func(t *testing.T) {
			m, _ := masterFixture(t)
			t.Setenv("S06_EARLY_SOCKET", "1")
			if both {
				t.Setenv("S06_EARLY_LINK", "1")
			}
			require.Error(t, m.Open(context.Background()))
			require.NotNil(t, m.cmd.ProcessState)
			entries, err := os.ReadDir(filepath.Dir(m.ControlPath()))
			require.NoError(t, err)
			require.Empty(t, entries, "reaped child artifacts must not depend on successful association")
		})
	}
}

func TestMasterUnknownEntryRefusesCleanup(t *testing.T) {
	m, _ := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	require.NoError(t, m.cmd.Process.Kill())
	<-m.connection.done
	unknown := filepath.Join(filepath.Dir(m.ControlPath()), "not-a-mux-entry")
	require.NoError(t, os.WriteFile(unknown, []byte("preserve"), 0600))
	require.Error(t, m.EnsureAlive(context.Background()))
	contents, err := os.ReadFile(unknown)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(contents))
	require.NoError(t, os.Remove(unknown))
	require.NoError(t, m.Close())
}

func TestMasterNeverOpenedPreservesOccupiedSocket(t *testing.T) {
	m, _ := masterFixture(t)
	foreign, err := net.Listen("unix", m.ControlPath())
	require.NoError(t, err)
	require.Error(t, m.Open(context.Background()))
	require.Error(t, m.Close(), "namespace is not empty and no child supplied cleanup authority")
	_, err = os.Lstat(m.ControlPath())
	require.NoError(t, err)
	require.NoError(t, foreign.Close())
	require.NoError(t, m.Close())
}
