//go:build linux || darwin

package ssh

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNamespaceRestrictedUmaskProcess(t *testing.T) {
	if os.Getenv("S06_RESTRICTIVE_UMASK") != "1" {
		return
	}
	old := syscall.Umask(0777)
	defer syscall.Umask(old)
	master, err := NewMaster("ssh://owned.invalid", slog.Default())
	if master != nil {
		dir := master.namespace.dir
		// Restore permission only on this fixture's allocated directory for cleanup
		// if the implementation incorrectly returned a partially usable Master.
		require.NoError(t, os.Chmod(dir, 0700))
		require.NoError(t, master.Close())
	}
	require.Error(t, err, "constructor must not return a namespace its own ownership checks reject")
	var pathErr *os.PathError
	require.True(t, errors.As(err, &pathErr))
	_, statErr := os.Lstat(pathErr.Path)
	require.True(t, os.IsNotExist(statErr), "failed constructor must remove its empty directory")
}

func TestNamespaceConstructorCleansPermissionFailure(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	child := exec.Command(executable, "-test.run=^TestNamespaceRestrictedUmaskProcess$")
	child.Env = append(os.Environ(), "S06_RESTRICTIVE_UMASK=1")
	output, err := child.CombinedOutput()
	require.NoError(t, err, string(output))
}
