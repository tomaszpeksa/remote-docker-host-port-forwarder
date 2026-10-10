//go:build linux || darwin

package instancepath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// This tests the anchored opening primitive, not a claim that arbitrary
// directory replacement is supported by the publisher ownership contract.
func TestOwnerOpenUsesSelectedDirectoryNotReboundName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "selected")
	require.NoError(t, os.Mkdir(dir, 0700))
	original := filepath.Join(dir, "owner.lock")
	require.NoError(t, os.WriteFile(original, []byte("preserve"), 0600))
	before, err := os.Stat(original)
	require.NoError(t, err)
	directory, err := openOwnerDirectory(dir)
	require.NoError(t, err)
	defer directory.Close()
	require.NoError(t, os.Rename(dir, dir+"-moved"))
	require.NoError(t, os.Mkdir(dir, 0700))
	file, err := openOwnerFile(directory)
	require.NoError(t, err)
	defer file.Close()
	actual, err := file.Stat()
	require.NoError(t, err)
	require.True(t, os.SameFile(before, actual), "must lock inside selected directory, not re-resolve its name")
	_, err = os.Lstat(filepath.Join(dir, "owner.lock"))
	require.True(t, os.IsNotExist(err), "rebound directory must remain untouched")
}

func TestOwnerLeafTypesAndExistingPermissions(t *testing.T) {
	for _, kind := range []string{"absolute-symlink", "relative-symlink", "directory", "fifo", "regular"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			dir := filepath.Join(home, ".rdhpf")
			require.NoError(t, os.Mkdir(dir, 0700))
			leaf := filepath.Join(dir, "owner.lock")
			target := filepath.Join(dir, "target")
			require.NoError(t, os.WriteFile(target, []byte("target stays"), 0600))
			switch kind {
			case "absolute-symlink":
				require.NoError(t, os.Symlink(target, leaf))
			case "relative-symlink":
				require.NoError(t, os.Symlink("target", leaf))
			case "directory":
				require.NoError(t, os.Mkdir(leaf, 0700))
			case "fifo":
				require.NoError(t, unix.Mkfifo(leaf, 0600))
			case "regular":
				require.NoError(t, os.WriteFile(leaf, []byte("existing content"), 0600))
				require.NoError(t, os.Chmod(leaf, 0640))
			}
			before, err := os.Lstat(leaf)
			require.NoError(t, err)
			owner, err := Acquire("ssh://configured-target")
			if kind == "regular" {
				require.NoError(t, err)
				require.NoError(t, owner.Close())
			} else {
				require.Error(t, err)
				require.Nil(t, owner)
			}
			after, err := os.Lstat(leaf)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			require.Equal(t, before.Mode(), after.Mode())
			data, err := os.ReadFile(target)
			require.NoError(t, err)
			require.Equal(t, "target stays", string(data))
			if kind == "regular" {
				data, err = os.ReadFile(leaf)
				require.NoError(t, err)
				require.Equal(t, "existing content", string(data))
			}
		})
	}
}

func TestOwnerStatUsesSelectedDirectoryNotReboundName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "selected")
	require.NoError(t, os.Mkdir(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "owner.lock"), []byte("original"), 0600))
	directory, err := openOwnerDirectory(dir)
	require.NoError(t, err)
	defer directory.Close()
	require.NoError(t, os.Rename(dir, dir+"-moved"))
	require.NoError(t, os.Mkdir(dir, 0700))
	// An unopenable leaf in the rebound name must not affect the selected inode.
	marker := filepath.Join(dir, "owner.lock")
	require.NoError(t, os.Mkdir(marker, 0700))
	before, err := os.Stat(marker)
	require.NoError(t, err)
	file, err := openOwnerFile(directory)
	require.NoError(t, err)
	defer file.Close()
	actual, err := file.Stat()
	require.NoError(t, err)
	original, err := os.Stat(filepath.Join(dir+"-moved", "owner.lock"))
	require.NoError(t, err)
	require.True(t, os.SameFile(original, actual))
	after, err := os.Stat(marker)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
}
