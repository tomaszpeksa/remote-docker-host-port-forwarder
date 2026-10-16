package manager

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
)

func TestRunStartupErrorJoinsPublicationBeforeAdmissionRelease(t *testing.T) {
	for _, fileOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "socket", true: "file-only"}[fileOnly], func(t *testing.T) {
			m, _, _ := recoveryManager(t)
			home, err := os.MkdirTemp("/tmp", "s06-publish-")
			require.NoError(t, err)
			defer os.RemoveAll(home)
			t.Setenv("HOME", home)
			owner, err := instancepath.Acquire(m.cfg.Host)
			require.NoError(t, err)
			defer owner.Close()
			socketPath, statePath, err := owner.Paths(m.cfg.Host)
			require.NoError(t, err)
			if fileOnly {
				require.NoError(t, os.WriteFile(socketPath, []byte("not-a-socket"), 0600))
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.ErrorIs(t, m.Run(ctx, owner), context.Canceled)
			if !fileOnly {
				require.NotNil(t, m.socketServer)
			}
			_, err = os.Stat(statePath)
			require.True(t, os.IsNotExist(err))
			require.NoError(t, owner.Close())
			next, err := instancepath.Acquire("ssh://successor")
			require.NoError(t, err)
			defer next.Close()
			require.NoError(t, os.WriteFile(statePath, []byte("successor"), 0600))
			require.Error(t, m.stateWriter.Write(nil, nil))
			require.NoError(t, m.stateWriter.Close())
			data, err := os.ReadFile(statePath)
			require.NoError(t, err)
			require.Equal(t, "successor", string(data))
			if fileOnly {
				data, err = os.ReadFile(socketPath)
				require.NoError(t, err)
				require.Equal(t, "not-a-socket", string(data))
			}
		})
	}
}
