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
		if _, err := os.Stat(dir); err != nil {
			joined <- fmt.Errorf("fixture must survive original Master Wait: %w", err)
			return
		}
		joined <- os.WriteFile(filepath.Join(records, "wait"), []byte(fmt.Sprintf("ORIGINAL_WAIT pid=%d Wait=%v ProcessState=%v ProcessStatePID=%d ExitCode=%d ECHILD=%v fixture-present=true\n", pid, waitErr, processState, processState.Pid(), processState.ExitCode(), childErr)), 0600)
	}()
	t.Cleanup(func() { h.close(); require.NoError(t, <-joined) })
	// A failed creation is intentional. Cleanup records whether the original
	// Master Wait receipt exists before removal; it never contacts Docker.
	script := `#!/bin/sh
printf '%s\n' "$@" > "$S12_TRANSFER_FAILURE_RECORDS/$1.args"
case "$1" in
run)
  case "$S12_TRANSFER_FAILURE_MODE" in
  command-failure) printf 'intentional creation failure\n' >&2; exit 125 ;;
  empty-id) exit 0 ;;
  malformed-id) printf 'not-a-container-id\n' ;;
  *) exit 23 ;;
  esac ;;
inspect) printf 'owned-transfer-failure\n' ;;
rm)
  printf 'CLEANUP_ENTERED\n' > "$S12_TRANSFER_FAILURE_RECORDS/cleanup-entry"
  i=0
  while [ ! -f "$S12_TRANSFER_FAILURE_RECORDS/wait" ] && [ "$i" -lt 100 ]; do
    /bin/sleep 0.01
    i=$((i+1))
  done
  if [ -f "$S12_TRANSFER_FAILURE_RECORDS/wait" ]; then
    printf 'WAIT_BEFORE_CLEANUP\n' > "$S12_TRANSFER_FAILURE_RECORDS/cleanup"
  else
    printf 'CLEANUP_BEFORE_WAIT\n' > "$S12_TRANSFER_FAILURE_RECORDS/cleanup"
  fi
  printf 'SIMULATED_REMOVAL %s\n' "$3" > "$S12_TRANSFER_FAILURE_RECORDS/removed" ;;
*) exit 23 ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700))
	switch os.Getenv("S12_TRANSFER_FAILURE_CREATOR") {
	case "shared-backend":
		h.backend()
	case "transfer-published":
		h.transferBackend(sshfixture.Nginx, 18090, "rdhpf.test-infrastructure=true")
	case "transfer-unbound":
		h.transferBackend(sshfixture.Alpine, 0, "rdhpf.forward.18090=ignored")
	default:
		t.Fatal("unknown backend creator")
	}
	t.Fatal("intentional creation failure unexpectedly returned")
}

func TestTransferBackendFailureJoinsBeforeCleanup(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	for _, creator := range []string{"shared-backend", "transfer-published", "transfer-unbound"} {
		for _, failure := range []struct {
			mode, diagnostic string
		}{
			{"command-failure", "intentional creation failure"},
			{"empty-id", `Expect "" to match`},
			{"malformed-id", `Expect "not-a-container-id" to match`},
		} {
			t.Run(creator+"/"+failure.mode, func(t *testing.T) {
				records := t.TempDir()
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestTransferBackendFailureProcess$", "-test.v")
				cmd.Env = append(os.Environ(), "S12_TRANSFER_FAILURE_PROCESS=1", "S12_TRANSFER_FAILURE_RECORDS="+records,
					"S12_TRANSFER_FAILURE_CREATOR="+creator, "S12_TRANSFER_FAILURE_MODE="+failure.mode)
				output, helperWaitErr := cmd.CombinedOutput()
				t.Logf("S12_FAILURE_HELPER output:\n%shelper original Wait=%v ProcessState=%v context=%v", output, helperWaitErr, cmd.ProcessState, ctx.Err())
				require.NoError(t, ctx.Err(), "owned subprocess timeout: %s", output)
				require.Error(t, helperWaitErr, "inner creation failure must remain a failure: %s", output)
				require.NotNil(t, cmd.ProcessState, "original helper Wait must complete")
				require.Equal(t, cmd.Process.Pid, cmd.ProcessState.Pid())
				t.Logf("S12_HELPER_WAIT_PID pid=%d ProcessStatePID=%d", cmd.Process.Pid, cmd.ProcessState.Pid())
				require.Equal(t, 1, cmd.ProcessState.ExitCode(), "%s", output)
				require.Contains(t, string(output), failure.diagnostic)
				if failure.mode == "command-failure" {
					require.Contains(t, string(output), "exit status 125")
				} else {
					require.Contains(t, string(output), "^[a-f0-9]{64}$")
				}
				read := func(name string) string {
					t.Helper()
					data, err := os.ReadFile(filepath.Join(records, name))
					require.NoError(t, err, "%s", output)
					t.Logf("S12_FAILURE_RECORD %s:\n%s", name, data)
					return string(data)
				}
				run := strings.Split(strings.TrimSuffix(read("run.args"), "\n"), "\n")
				var name string
				switch creator {
				case "shared-backend":
					require.Len(t, run, 9)
					name = run[3]
					require.Regexp(t, `^owned-transfer-failure-[1-9][0-9]*$`, name)
					port := strings.TrimPrefix(name, "owned-transfer-failure-")
					require.Equal(t, []string{"run", "-d", "--name", name, "--label", "rdhpf.trust=owned-transfer-failure", "-p", "172.17.0.1:" + port + ":80", sshfixture.Nginx}, run)
				case "transfer-published":
					name = "owned-transfer-failure-18090"
					require.Equal(t, []string{"run", "-d", "--name", name, "--label", "rdhpf.trust=owned-transfer-failure", "--label", "rdhpf.test-infrastructure=true", "-p", "172.17.0.1:18090:80", sshfixture.Nginx}, run)
				case "transfer-unbound":
					name = "owned-transfer-failure-0-1"
					require.Equal(t, []string{"run", "-d", "--name", name, "--label", "rdhpf.trust=owned-transfer-failure", "--label", "rdhpf.forward.18090=ignored", sshfixture.Alpine, "sleep", "300"}, run)
				}
				require.Equal(t, "inspect\n--format\n{{index .Config.Labels \"rdhpf.trust\"}}\n"+name+"\n", read("inspect.args"))
				require.Equal(t, "rm\n-f\n"+name+"\n", read("rm.args"))
				require.Equal(t, "CLEANUP_ENTERED\n", read("cleanup-entry"))
				require.Equal(t, "SIMULATED_REMOVAL "+name+"\n", read("removed"))
				wait := read("wait")
				require.Contains(t, wait, "ORIGINAL_WAIT pid=")
				cleanup := read("cleanup")
				require.Equal(t, "WAIT_BEFORE_CLEANUP\n", cleanup, "FAILED_CREATION_MUST_JOIN_BEFORE_BACKEND_CLEANUP: %s", output)
				t.Logf("S12_TRANSFER_FAILURE_ORDER %s %s", strings.TrimSpace(wait), strings.TrimSpace(cleanup))
			})
		}
	}
}
