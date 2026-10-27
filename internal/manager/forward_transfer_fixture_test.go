//go:build rdhpf_integration

package manager

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/instancepath"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/ssh"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/muxstub"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/testutil/sshfixture"
)

func TestTransferBackendFailureProcess(t *testing.T) {
	if os.Getenv("S12_TRANSFER_FAILURE_PROCESS") != "1" {
		t.Skip("owned failure subprocess only")
	}
	records := os.Getenv("S12_TRANSFER_FAILURE_RECORDS")
	dir := muxstub.Install(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	master, err := ssh.NewMaster("ssh://fixture.invalid", logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, master.Close()) })
	require.NoError(t, master.Open(context.Background()))
	owner, err := instancepath.Acquire("ssh://fixture.invalid")
	require.NoError(t, err)
	_, cancel := context.WithCancel(context.Background())
	f := &sshfixture.Fixture{T: t, Alias: "owned-transfer-failure", Marker: filepath.Join(dir, "absent-prompt")}
	h := &discoveryOwned{t: t, f: f, m: &Manager{sshMaster: master}, owner: owner, cancel: cancel}
	receipt := master.TestWaitReceipt()
	joined := make(chan error, 1)
	go func() {
		pid, waitErr, processState := receipt()
		var status syscall.WaitStatus
		_, childErr := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
		if pid <= 0 || processState == nil || processState.Pid() != pid || childErr != syscall.ECHILD {
			joined <- fmt.Errorf("original Wait receipt invalid: pid=%d Wait=%v state=%v ECHILD=%v", pid, waitErr, processState, childErr)
			return
		}
		joined <- os.WriteFile(filepath.Join(records, "wait"), []byte(fmt.Sprintf("ORIGINAL_WAIT pid=%d Wait=%v ProcessState=%v ExitCode=%d ECHILD=%v\n", pid, waitErr, processState, processState.ExitCode(), childErr)), 0600)
	}()
	t.Cleanup(func() { h.close(); require.NoError(t, <-joined) })
	// A failed creation is intentional. Cleanup records whether the original
	// Master Wait receipt exists before removal; it never contacts Docker.
	script := `#!/bin/sh
case "$1" in
run) printf 'intentional creation failure\n' >&2; exit 125 ;;
inspect) printf 'owned-transfer-failure\n' ;;
rm)
  i=0
  while [ ! -f "$S12_TRANSFER_FAILURE_RECORDS/wait" ] && [ "$i" -lt 100 ]; do
    /bin/sleep 0.01
    i=$((i+1))
  done
  if [ -f "$S12_TRANSFER_FAILURE_RECORDS/wait" ]; then
    printf 'WAIT_BEFORE_CLEANUP\n' > "$S12_TRANSFER_FAILURE_RECORDS/cleanup"
  else
    printf 'CLEANUP_BEFORE_WAIT\n' > "$S12_TRANSFER_FAILURE_RECORDS/cleanup"
  fi ;;
*) exit 23 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700))
	h.transferBackend(sshfixture.Alpine, 0)
	t.Fatal("intentional creation failure unexpectedly returned")
}

func TestTransferBackendFailureJoinsBeforeCleanup(t *testing.T) {
	records := t.TempDir()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestTransferBackendFailureProcess$", "-test.v")
	cmd.Env = append(os.Environ(), "S12_TRANSFER_FAILURE_PROCESS=1", "S12_TRANSFER_FAILURE_RECORDS="+records)
	output, helperWaitErr := cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "owned subprocess timeout: %s", output)
	require.Error(t, helperWaitErr, "inner creation failure must remain a failure: %s", output)
	require.NotNil(t, cmd.ProcessState, "original helper Wait must complete")
	require.Equal(t, 1, cmd.ProcessState.ExitCode(), "%s", output)
	require.Contains(t, string(output), "intentional creation failure")
	wait, err := os.ReadFile(filepath.Join(records, "wait"))
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(wait), "ORIGINAL_WAIT pid=")
	cleanup, err := os.ReadFile(filepath.Join(records, "cleanup"))
	require.NoError(t, err, "%s", output)
	require.Equal(t, "WAIT_BEFORE_CLEANUP\n", string(cleanup), "FAILED_CREATION_MUST_JOIN_BEFORE_BACKEND_CLEANUP: %s", output)
	t.Logf("S12_TRANSFER_FAILURE_ORDER %s%shelper original Wait=%v ProcessState=%v", strings.TrimSpace(string(wait)), " "+strings.TrimSpace(string(cleanup))+" ", helperWaitErr, cmd.ProcessState)
}
