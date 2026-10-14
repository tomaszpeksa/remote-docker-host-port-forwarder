//go:build linux || darwin

package instancepath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestOwnerFailedLockPreservesOwnerAndPersistentFile(t *testing.T) {
	dir := t.TempDir()
	directory, err := openOwnerDirectory(dir)
	require.NoError(t, err)
	defer directory.Close()
	owner, err := openOwnerFile(directory)
	require.NoError(t, err)
	defer owner.Close()
	before, err := owner.Stat()
	require.NoError(t, err)

	contender, err := openOwnerFile(directory)
	require.Nil(t, contender)
	require.EqualError(t, err, "another rdhpf publisher already owns this status directory")
	// Failed acquisition must neither unlock the existing owner nor remove its inode.
	contender, err = openOwnerFile(directory)
	require.Nil(t, contender)
	require.EqualError(t, err, "another rdhpf publisher already owns this status directory")
	after, err := os.Stat(filepath.Join(dir, "owner.lock"))
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	require.NoError(t, owner.Close())
	next, err := openOwnerFile(directory)
	require.NoError(t, err)
	require.NoError(t, next.Close())
}

func TestOwnerDirectoryErrorRetainsIdentity(t *testing.T) {
	directory, err := openOwnerDirectory(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, directory.Close())
	file, err := openOwnerFile(directory)
	require.Nil(t, file)
	require.ErrorIs(t, err, unix.EBADF)
	var pathErr *os.PathError
	require.ErrorAs(t, err, &pathErr)
	require.Equal(t, "statat", pathErr.Op)
	require.Equal(t, "owner.lock", pathErr.Path)
}
