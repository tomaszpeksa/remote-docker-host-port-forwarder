package reconcile

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/muxstub"
)

func TestMuxProcess(t *testing.T) { muxstub.Process(t) }

type obligationRun struct {
	r       *Reconciler
	st      *state.State
	history *state.History
	master  *ssh.Master
	port    int
	dir     string
	now     time.Time
}

func obligationFixture(t *testing.T) *obligationRun {
	t.Helper()
	dir := muxstub.Install(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	master, err := ssh.NewMaster("ssh://fixture.invalid", logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, master.Close(), "owned Master Wait before fixture retirement") })
	require.NoError(t, master.Open(context.Background()))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	run := &obligationRun{st: state.NewState(), history: state.NewHistory(), master: master, port: listener.Addr().(*net.TCPAddr).Port, dir: dir, now: time.Now()}
	run.r = NewReconciler(run.st, run.history, logger)
	run.r.now = func() time.Time { return run.now }
	return run
}
func (r *obligationRun) apply(retry bool) error {
	return r.master.WithForwarding(context.Background(), func(connection *ssh.Forwarding) error {
		r.r.RetireBefore(connection.Generation())
		actions := r.r.RetryActions()
		if !retry {
			add, remove := r.r.Diff()
			actions = append(remove, add...)
		}
		return r.r.Apply(context.Background(), connection, actions)
	})
}
func (r *obligationRun) calls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, "calls"))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}
func (r *obligationRun) fail(t *testing.T, operation string) string {
	t.Helper()
	path := filepath.Join(r.dir, "fail-"+operation)
	// The Master inherits variables at startup, so use its existing parent-owned
	// control directory rather than mutating a child environment after Open.
	require.NoError(t, os.WriteFile(path, []byte("injected rejection"), 0600))
	return path
}

func TestFailedCancelKeepsOwnership(t *testing.T) {
	run := obligationFixture(t)
	run.st.SetDesired("owner", []int{run.port})
	require.NoError(t, run.apply(false))
	created := run.st.GetByContainer("owner")[0].CreatedAt
	failure := run.fail(t, "CLOSE")
	run.st.SetDesired("owner", nil)
	require.Error(t, run.apply(false), "FAILED_CANCEL_MUST_RETAIN_OBLIGATION")
	require.Len(t, run.st.GetByContainer("owner"), 1, "FAILED_CANCEL_MUST_RETAIN_OBLIGATION")
	require.Empty(t, run.history.GetAll(), "NO_HISTORY_BEFORE_CONFIRMED_REMOVAL")
	resource, ok := run.st.Resource("owner", run.port)
	require.True(t, ok)
	require.Equal(t, state.ForwardOwned, resource.Ownership)
	require.True(t, resource.Remove)
	require.Equal(t, created, run.st.GetByContainer("owner")[0].CreatedAt)
	require.NoError(t, os.Remove(failure))
	before := run.calls(t)
	require.NoError(t, run.apply(true))
	require.Equal(t, before, run.calls(t), "retry not eligible yet")
	run.now = run.now.Add(time.Second)
	require.NoError(t, run.apply(true))
	require.Empty(t, run.st.GetResources())
	require.Empty(t, run.st.GetActual())
	require.Len(t, run.history.GetAll(), 1)
	require.NoError(t, run.apply(false))
	require.Len(t, run.history.GetAll(), 1)
}

func TestPendingOwnedForwardRemovedOnStop(t *testing.T) {
	run := obligationFixture(t)
	run.r.probe = func(context.Context, int) error { return fmt.Errorf("injected readiness observation failure") }
	run.st.SetDesired("owner", []int{run.port})
	require.Error(t, run.apply(false))
	require.Equal(t, "pending", run.st.GetByContainer("owner")[0].Status)
	run.now = run.now.Add(time.Second)
	require.Error(t, run.apply(true))
	require.Equal(t, 1, strings.Count(run.calls(t), "OPEN"), "owned pending must retry probe, not OPEN")
	run.st.SetDesired("owner", nil)
	run.now = run.now.Add(time.Second)
	require.NoError(t, run.apply(false))
	require.Contains(t, run.calls(t), "CLOSE", "OWNED_PENDING_STOP_MUST_CANCEL")
	require.Empty(t, run.st.GetActual())
	require.Len(t, run.history.GetAll(), 1)
}

func TestUnknownForwardEffectWaitsForNaturalRetirement(t *testing.T) {
	for _, operation := range []string{"OPEN", "CLOSE"} {
		t.Run(operation, func(t *testing.T) {
			run := obligationFixture(t)
			run.st.SetDesired("owner", []int{run.port})
			if operation == "CLOSE" {
				require.NoError(t, run.apply(false))
				run.st.SetDesired("owner", nil)
			}
			lost := filepath.Join(run.dir, "lost-reply")
			require.NoError(t, os.WriteFile(lost, nil, 0600))
			require.Error(t, run.apply(false))
			resource, ok := run.st.Resource("owner", run.port)
			require.True(t, ok)
			require.Equal(t, state.ForwardUnknown, resource.Ownership)
			require.NoError(t, os.Remove(lost))
			before := run.calls(t)
			run.now = run.now.Add(time.Hour)
			require.NoError(t, run.apply(true))
			require.Equal(t, before, run.calls(t), "unknown work must never replay")
			require.Empty(t, run.history.GetAll())
			run.st.SetDesired("replacement", []int{run.port})
			require.Error(t, run.apply(false))
			require.Equal(t, before, run.calls(t), "unknown owner blocks same-port replacement")
			require.NoError(t, run.master.Close())
			require.NoError(t, run.master.Open(context.Background()))
			run.st.SetDesired("owner", nil)
			require.NoError(t, run.apply(false))
			require.Equal(t, "active", run.st.GetByContainer("replacement")[0].Status)
			require.Len(t, run.history.GetAll(), 1, "retirement consumes stopped unknown once")
		})
	}
}

func TestStoppedUnownedConflictNeverCancels(t *testing.T) {
	run := obligationFixture(t)
	run.fail(t, "OPEN")
	run.st.SetDesired("owner", []int{run.port})
	require.Error(t, run.apply(false))
	run.st.SetDesired("owner", nil)
	require.NoError(t, run.apply(false))
	require.NotContains(t, run.calls(t), "CLOSE")
	require.Empty(t, run.st.GetActual())
	require.Len(t, run.history.GetAll(), 1)
}
