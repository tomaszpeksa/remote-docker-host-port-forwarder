package instancepath

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOwnerDescriptorNotInherited(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	owner, err := Acquire("ssh://one")
	require.NoError(t, err)
	defer owner.Close()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAdmissionProcess$")
	cmd.Env = append(os.Environ(), "RDHPF_ADMISSION_HELPER=idle")
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	output, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() { input.Close(); require.NoError(t, cmd.Wait()) }()
	line, err := bufio.NewReader(output).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", line)
	// Close (not LOCK_UN) releases our only descriptor. An inherited descriptor
	// in the still-live helper would keep the flock and make admission fail.
	require.NoError(t, owner.Close())
	next, err := Acquire("ssh://two")
	require.NoError(t, err)
	require.NoError(t, next.Close())
}
