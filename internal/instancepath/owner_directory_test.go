//go:build linux || darwin

package instancepath

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestOwnerDirectoryDescriptorProperties(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(dir, alias))
	directory, err := openOwnerDirectory(alias)
	require.NoError(t, err)
	defer directory.Close()
	expected, err := os.Stat(dir)
	require.NoError(t, err)
	actual, err := directory.Stat()
	require.NoError(t, err)
	require.True(t, os.SameFile(expected, actual))
	flags, err := unix.FcntlInt(directory.Fd(), unix.F_GETFD, 0)
	require.NoError(t, err)
	require.NotZero(t, flags&unix.FD_CLOEXEC)
	file, err := openOwnerFile(directory)
	require.NoError(t, err)
	defer file.Close()
	flags, err = unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
	require.NoError(t, err)
	require.NotZero(t, flags&unix.FD_CLOEXEC)
	require.NoError(t, directory.Close())
	other, err := openOwnerFile(directory)
	require.Error(t, err)
	require.Nil(t, other)
	for _, kind := range []string{"regular", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), kind)
			if kind == "fifo" {
				require.NoError(t, unix.Mkfifo(path, 0600))
			} else {
				require.NoError(t, os.WriteFile(path, nil, 0600))
			}
			rejected, err := openOwnerDirectory(path)
			require.Error(t, err)
			require.Nil(t, rejected)
		})
	}
}

// Count only descriptors referring to our fixture inodes, not unrelated runtime
// descriptors. /dev/fd is available on the supported local Linux/macOS targets.
func countFixtureDescriptors(t *testing.T, identities ...os.FileInfo) int {
	t.Helper()
	directory, err := os.Open("/dev/fd")
	require.NoError(t, err)
	defer directory.Close()
	// Read names without lstat: Darwin's fd directory can expose invalid slots.
	names, err := directory.Readdirnames(-1)
	require.NoError(t, err)
	count := 0
	for _, name := range names {
		fd, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		var info unix.Stat_t
		if unix.Fstat(fd, &info) != nil {
			continue
		}
		for _, identity := range identities {
			stat := identity.Sys().(*syscall.Stat_t)
			if stat.Dev == info.Dev && stat.Ino == info.Ino {
				count++
				break
			}
		}
	}
	return count
}

func TestOwnerAcquireClosesDirectoryAndRejectedDescriptors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	owner, err := Acquire("ssh://first")
	require.NoError(t, err)
	defer owner.Close()
	directory, err := os.Stat(filepath.Join(home, ".rdhpf"))
	require.NoError(t, err)
	leaf, err := os.Stat(filepath.Join(home, ".rdhpf", "owner.lock"))
	require.NoError(t, err)
	require.Equal(t, 1, countFixtureDescriptors(t, directory, leaf), "only the retained flock descriptor may remain")
	for i := 0; i < 20; i++ {
		loser, err := Acquire("ssh://other")
		require.Error(t, err)
		require.Nil(t, loser)
	}
	require.Equal(t, 1, countFixtureDescriptors(t, directory, leaf), "rejected acquisitions must close directory and leaf descriptors")
	require.NoError(t, owner.Close())
	require.Equal(t, 0, countFixtureDescriptors(t, directory, leaf))
	// With no active owner, replace this fixture's leaf by a refused entry.
	require.NoError(t, os.Remove(filepath.Join(home, ".rdhpf", "owner.lock")))
	require.NoError(t, os.Mkdir(filepath.Join(home, ".rdhpf", "owner.lock"), 0700))
	loser, err := Acquire("ssh://other")
	require.Error(t, err)
	require.Nil(t, loser)
	require.Equal(t, 0, countFixtureDescriptors(t, directory), "pre-open rejection must close the directory descriptor")
}

func TestOwnerDirectoryPermissionBehavior(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission checks require an unprivileged fixture process")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".rdhpf")
	require.NoError(t, os.Mkdir(dir, 0700))
	defer os.Chmod(dir, 0700)
	require.NoError(t, os.Chmod(dir, 0500))
	owner, err := Acquire("ssh://test")
	require.ErrorIs(t, err, os.ErrPermission)
	require.Nil(t, owner)
	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, 0, countFixtureDescriptors(t, info))
	require.NoError(t, os.Chmod(dir, 0700))
	owner, err = Acquire("ssh://test")
	require.NoError(t, err)
	require.NoError(t, owner.Close())
	require.NoError(t, os.Chmod(dir, 0500))
	owner, err = Acquire("ssh://test")
	require.NoError(t, err)
	require.NoError(t, owner.Close())
	require.NoError(t, os.Chmod(dir, 0100))
	owner, err = Acquire("ssh://test")
	require.ErrorIs(t, err, os.ErrPermission)
	require.Nil(t, owner)
}
