package reconcile

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"

	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/socket"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/statefile"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/status"
)

func TestReconcilerSafeReasons(t *testing.T) {
	t.Run("rejection", func(t *testing.T) {
		run := obligationFixture(t)
		path := run.fail(t, "OPEN")
		require.NoError(t, os.WriteFile(path, []byte("private-user@192.0.2.77 /private-control private-payload"), 0600))
		run.st.SetDesired("functional-owner", []int{run.port})
		err := run.apply(false)
		require.ErrorContains(t, err, "private-payload", "private rejection must survive presentation")
		actual := run.st.GetActual()
		require.Len(t, actual, 1)
		require.Equal(t, "functional-owner", actual[0].ContainerID)
		require.Equal(t, "forward creation failed", actual[0].Reason)
		writer := statefile.NewWriterAtPath("ssh://functional-host", filepath.Join(run.dir, "state.json"), time.Now())
		defer writer.Close()
		require.NoError(t, writer.Write(actual, nil))
		b, err := os.ReadFile(filepath.Join(run.dir, "state.json"))
		require.NoError(t, err)
		socketPath := filepath.Join(run.dir, "status.sock")
		server, err := socket.NewServerAtPath("ssh://functional-host", socketPath, run.st, run.history, time.Now(), run.r.logger)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		defer func() { cancel(); require.NoError(t, server.Close()); require.NoError(t, <-done) }()
		go func() { done <- server.Start(ctx) }()
		conn, err := net.DialTimeout("unix", socketPath, time.Second)
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
		var snapshot statefile.StateFile
		require.NoError(t, json.NewDecoder(conn).Decode(&snapshot))
		socketJSON, err := json.Marshal(snapshot)
		require.NoError(t, err)
		for _, output := range []string{string(b), string(socketJSON)} {
			require.NotContains(t, output, "private-")
			require.NotContains(t, output, "192.0.2.77")
			require.Contains(t, output, "functional-owner")
			require.Contains(t, output, "functional-host")
			require.Contains(t, output, "forward creation failed")
		}
		f := []status.Forward{{ContainerID: actual[0].ContainerID, LocalPort: actual[0].Port, Reason: actual[0].Reason}}
		for _, out := range []string{status.FormatJSON(f), status.FormatYAML(f), status.FormatTable(f)} {
			require.NotContains(t, out, "private-")
			require.Contains(t, out, "forward creation failed")
		}
	})
	t.Run("transfer", func(t *testing.T) {
		run := obligationFixture(t)
		run.st.SetDesired("old-owner", []int{run.port})
		require.NoError(t, run.apply(false))
		created := run.st.GetActual()[0].CreatedAt
		run.st.SetDesired("old-owner", nil)
		run.st.SetDesired("private-replacement-id", []int{run.port})
		require.NoError(t, run.apply(false))
		history := run.history.GetAll()
		require.Len(t, history, 1)
		require.Equal(t, created, history[0].StartedAt)
		require.Equal(t, "port claimed by another container", history[0].EndReason)
		require.Equal(t, "private-replacement-id", run.st.GetActual()[0].ContainerID, "functional identity remains full")
	})
}
