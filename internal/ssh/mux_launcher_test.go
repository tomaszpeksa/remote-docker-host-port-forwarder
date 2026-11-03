package ssh

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/muxstub"
	"golang.org/x/sys/unix"
)

func TestMuxProcess(t *testing.T) { muxstub.Process(t) }

func TestInstalledMuxLauncherWaitBeforeCleanup(t *testing.T) {
	var directory string
	var child *masterConnection
	t.Run("owned", func(t *testing.T) {
		directory = muxstub.Install(t)
		master, err := NewMaster("ssh://fixture.invalid", quietForwardLogger())
		require.NoError(t, err)
		t.Cleanup(func() {
			require.NoError(t, master.Close())
			if child == nil {
				return // Open already failed; Close still precedes fixture cleanup.
			}
			select {
			case <-child.done:
			default:
				t.Fatal("original Master Wait must finish before fixture cleanup")
			}
			require.NotNil(t, child.cmd.ProcessState)
			require.Equal(t, child.cmd.Process.Pid, child.cmd.ProcessState.Pid())
			var status unix.WaitStatus
			_, waitErr := unix.Wait4(child.cmd.Process.Pid, &status, unix.WNOHANG, nil)
			require.ErrorIs(t, waitErr, unix.ECHILD, "original child was already reaped by Master Wait")
			_, statErr := os.Lstat(directory)
			require.NoError(t, statErr, "fixture must survive through original Master Wait")
			t.Logf("owned mux child pid=%d original Wait=%v ProcessState=%v subsequent Wait4=%v; fixture still present", child.cmd.Process.Pid, child.waitErr, child.cmd.ProcessState, waitErr)
		})
		require.NoError(t, master.Open(context.Background()))
		child = master.connection
		require.NotNil(t, child)
		require.Equal(t, filepath.Join(directory, "ssh"), child.cmd.Path)
	})
	require.NotNil(t, child)
	require.NotNil(t, child.cmd.ProcessState)
	_, err := os.Lstat(directory)
	require.True(t, os.IsNotExist(err), "fixture cleanup follows joined original child")
}
