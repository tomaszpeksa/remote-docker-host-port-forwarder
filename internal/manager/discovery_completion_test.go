//go:build rdhpf_integration

package manager

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

// A private subprocess deliberately fails after observing actual Run completion.
// Cleanup must independently join that completion and release admission before
// process exit; an automatic OS lock release cannot satisfy the marker.
func TestDiscoveryOwnedCompletionObservers(t *testing.T) {
	if os.Getenv("S09_COMPLETION_OBSERVER_CHILD") == "1" {
		m, _, _ := recoveryManager(t) // private inert SSH; never opens a connection
		home, err := os.MkdirTemp("/tmp", "s09-join-")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, os.RemoveAll(home)) })
		t.Setenv("HOME", home)
		owner, err := instancepath.Acquire(m.cfg.Host)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h := &discoveryOwned{t: t, m: m, owner: owner, ctx: ctx, cancel: cancel, f: &sshfixture.Fixture{T: t, Marker: filepath.Join(home, "no-prompt")}}
		t.Cleanup(func() {
			successor, err := instancepath.Acquire(m.cfg.Host)
			require.NoError(t, err, "admission must be released before child exit")
			require.NoError(t, successor.Close())
			fmt.Println("OWNED_OWNER_RELEASED_BEFORE_EXIT")
		})
		h.start() // registers joined cleanup after the release-check cleanup
		<-h.done  // first observer; cleanup is the second observer
		t.Fatal("intentional failure after actual canceled Run completion")
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDiscoveryOwnedCompletionObservers$", "-test.v")
	cmd.Env = append(os.Environ(), "S09_COMPLETION_OBSERVER_CHILD=1")
	output, err := cmd.CombinedOutput()
	t.Logf("expected-failing private child:\n%s", output)
	require.NoError(t, ctx.Err(), "subprocess timeout is not cleanup proof")
	require.Error(t, err, "child intentionally fails")
	require.Contains(t, string(output), "OWNED_OWNER_RELEASED_BEFORE_EXIT", "CLEANUP_COMPLETION_FANOUT")
	require.NotContains(t, string(output), "did not join")
}
