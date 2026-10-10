package ssh

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMasterCrashDoesNotTouchOtherMaster(t *testing.T) {
	a, _ := masterFixture(t)
	require.NoError(t, a.Open(context.Background()))
	b, _ := masterFixture(t)
	require.NoError(t, b.Open(context.Background()))
	before, err := os.Lstat(b.ControlPath())
	require.NoError(t, err)
	bPID := b.cmd.Process.Pid
	require.NoError(t, a.cmd.Process.Kill())
	<-a.connection.done
	require.NoError(t, a.EnsureAlive(context.Background()))
	require.NoError(t, a.Close())
	after, err := os.Lstat(b.ControlPath())
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after))
	require.Equal(t, bPID, b.cmd.Process.Pid)
	require.NoError(t, b.Check())
}
