package ssh

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNamespaceLivePeerRefusalPreservesAllEntries(t *testing.T) {
	n, err := newMasterNamespace()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(n.dir)) })
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: n.path, Net: "unix"})
	require.NoError(t, err)
	stale.SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())
	foreignPath := filepath.Join(n.dir, "mux.aaaaaaaaaaaaaaaa")
	foreign, err := net.ListenUnix("unix", &net.UnixAddr{Name: foreignPath, Net: "unix"})
	require.NoError(t, err)
	foreign.SetUnlinkOnClose(false)
	defer foreign.Close()
	before, err := os.Lstat(n.path)
	require.NoError(t, err)
	foreignBefore, err := os.Lstat(foreignPath)
	require.NoError(t, err)

	err = n.reclaim()
	require.ErrorContains(t, err, "live foreign SSH endpoint after child retirement")
	require.True(t, n.owned)
	after, err := os.Lstat(n.path)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "refusal must preserve even an earlier stale entry")
	foreignAfter, err := os.Lstat(foreignPath)
	require.NoError(t, err)
	require.True(t, os.SameFile(foreignBefore, foreignAfter))
	require.NoError(t, foreign.Close())
	require.NoError(t, n.reclaim())
	require.NoError(t, n.close())
}
