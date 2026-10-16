package instancepath

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAdmissionProcess(t *testing.T) {
	if os.Getenv("RDHPF_ADMISSION_HELPER") == "idle" {
		fmt.Println("ready")
		_, err := bufio.NewReader(os.Stdin).ReadByte()
		if err != nil {
			require.ErrorIs(t, err, io.EOF, "release must be a byte or closed input")
		}
		return
	}
	if os.Getenv("RDHPF_ADMISSION_HELPER") != "1" {
		return
	}
	owner, err := Acquire("ssh://other-target")
	if err != nil {
		fmt.Println("refused")
		return
	}
	defer owner.Close()
	fmt.Println("admitted")
	_, err = bufio.NewReader(os.Stdin).ReadByte()
	if err != nil {
		require.ErrorIs(t, err, io.EOF, "release must be a byte or closed input")
	}
}

func TestOwnerExcludesAllTargetsAndAliases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a, err := Acquire("ssh://first")
	require.NoError(t, err)
	defer a.Close()
	socket, state, err := a.Paths("ssh://first")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".rdhpf"), filepath.Dir(socket))
	require.Equal(t, filepath.Dir(socket), filepath.Dir(state))
	_, _, err = a.Paths("ssh://other")
	require.Error(t, err)
	_, err = Acquire("ssh://other-target")
	require.Error(t, err)
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(home, alias))
	t.Setenv("HOME", alias)
	_, err = Acquire("ssh://third-target")
	require.Error(t, err)
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestAdmissionProcess$")
	cmd.Env = append(os.Environ(), "RDHPF_ADMISSION_HELPER=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(output), "refused")
	lock := filepath.Join(home, ".rdhpf", "owner.lock")
	before, err := os.Lstat(lock)
	require.NoError(t, err)
	require.NoError(t, a.Close())
	require.NoError(t, a.Close())
	b, err := Acquire("ssh://different")
	require.NoError(t, err)
	defer b.Close()
	after, err := os.Lstat(lock)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "persistent inode must not be unlinked")
	t.Setenv("HOME", t.TempDir())
	c, err := Acquire("ssh://different")
	require.NoError(t, err)
	require.NoError(t, c.Close())
}

func TestOwnerRefusesSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".rdhpf"), 0700))
	target := filepath.Join(home, "target")
	require.NoError(t, os.WriteFile(target, []byte("untouched"), 0600))
	require.NoError(t, os.Symlink(target, filepath.Join(home, ".rdhpf", "owner.lock")))
	_, err := Acquire("ssh://test")
	require.Error(t, err)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "untouched", string(data))
}
