package ssh

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMasterObservedDirectoryReplacementRefusesCleanup(t *testing.T) {
	m, _ := masterFixture(t)
	require.NoError(t, m.Open(context.Background()))
	original := m.namespace.dir
	backup := original + ".saved"
	require.NoError(t, os.Rename(original, backup))
	restored := false
	defer func() {
		if !restored {
			_ = os.Remove(original)
			require.NoError(t, os.Rename(backup, original))
		}
	}()
	require.NoError(t, os.Mkdir(original, 0700))
	foreign, err := net.Listen("unix", m.ControlPath())
	require.NoError(t, err)
	defer foreign.Close()
	require.ErrorContains(t, m.Check(), "ownership lost")
	require.ErrorContains(t, m.Close(), "ownership lost")
	require.NotNil(t, m.cmd.ProcessState, "retirement still reaps the actual owned child")
	_, err = os.Lstat(m.ControlPath())
	require.NoError(t, err, "observed replacement must survive")
	require.NoError(t, foreign.Close())
	require.NoError(t, os.Remove(original))
	require.NoError(t, os.Rename(backup, original))
	restored = true
	require.NoError(t, m.Close())
}
