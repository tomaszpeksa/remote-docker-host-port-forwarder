//go:build rdhpf_integration

package ssh

import (
	"context"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/muxstub"
)

func TestRetireExitedChildRequiresOriginalWait(t *testing.T) {
	muxstub.Install(t)
	master, err := NewMaster("ssh://fixture.invalid", quietForwardLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, master.Close()) })
	ctx := context.Background()
	require.ErrorContains(t, master.TestRetireExitedChild(ctx), "no retained child")
	require.NoError(t, master.Open(ctx))
	owned := master.connection
	generation := master.generation
	require.ErrorContains(t, master.TestRetireExitedChild(ctx), "still live")
	require.Same(t, owned, master.connection)
	require.Equal(t, generation, master.generation)
	require.NoError(t, master.Check(), "live-child refusal must neither signal nor retire")
	receipt := master.TestWaitReceipt()
	_, pid, err := master.TestKillOwnedChild(ctx)
	require.NoError(t, err)
	require.NoError(t, master.TestRetireExitedChild(ctx))
	require.Nil(t, master.connection)
	require.Equal(t, generation+1, master.generation)
	waitPID, waitErr, processState := receipt()
	require.Equal(t, pid, waitPID)
	require.NotNil(t, processState)
	var status syscall.WaitStatus
	_, err = syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	require.ErrorIs(t, err, syscall.ECHILD)
	t.Logf("RETIRED_EXITED_CHILD original_pid=%d Wait=%v ProcessState=%v ECHILD=%v generation=%d", pid, waitErr, processState, err, master.generation)
	require.ErrorContains(t, master.TestRetireExitedChild(ctx), "no retained child")
	require.NoError(t, master.EnsureAlive(ctx))
	require.Equal(t, generation+1, master.generation, "opening a successor does not consume another retirement")
}
