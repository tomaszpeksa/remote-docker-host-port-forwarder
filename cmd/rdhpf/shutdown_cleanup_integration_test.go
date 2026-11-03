//go:build rdhpf_integration

package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The controller's retained children and the CLI's original Wait are separate
// obligations. A diagnostic alarm must never release this owning cleanup frame.
func joinShutdownCLI(t *testing.T, cmd *exec.Cmd, done <-chan struct{}, release, closeController func(), diagnostic <-chan time.Time) {
	t.Helper()
	defer func() {
		<-done
		t.Log("S13_CLI_JOINED original CLI Wait after controller join")
	}()
	select {
	case <-done:
	default:
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	release()
	closeController()
	select {
	case <-done:
	case <-diagnostic:
		t.Error("S13_CLI_JOIN_DIAGNOSTIC: retaining original CLI Wait until completion")
	}
}

func TestShutdownCLIJoinProcess(t *testing.T) {
	if os.Getenv("RDHPF_S13_JOIN_PROCESS") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	_, _ = os.Stdout.WriteString("ready\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestShutdownCLIJoinDiagnosticRetainsWait(t *testing.T) {
	if os.Getenv("RDHPF_S13_JOIN_CONTROL") == "1" {
		executable, err := os.Executable()
		require.NoError(t, err)
		cmd := exec.Command(executable, "-test.run=^TestShutdownCLIJoinProcess$")
		cmd.Env = append(os.Environ(), "RDHPF_S13_JOIN_PROCESS=1", "GORACE=atexit_sleep_ms=0")
		input, err := cmd.StdinPipe()
		require.NoError(t, err)
		output, err := cmd.StdoutPipe()
		require.NoError(t, err)
		require.NoError(t, cmd.Start())
		done := make(chan struct{})
		var result error
		go func() { result = cmd.Wait(); close(done) }()
		var released chan struct{}
		t.Cleanup(func() {
			_ = input.Close()
			<-done
			if released != nil {
				<-released
			}
			require.NoError(t, result)
			t.Log("S13_ENV_RETIREMENT after original child completion")
		})
		line, err := bufio.NewReader(output).ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "ready\n", line)
		released = make(chan struct{})
		go func() { defer close(released); time.Sleep(50 * time.Millisecond); _ = input.Close() }()
		diagnostic := make(chan time.Time, 1)
		diagnostic <- time.Now()
		joinShutdownCLI(t, cmd, done, func() {}, func() {}, diagnostic)
		return
	}
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.v", "-test.run=^TestShutdownCLIJoinDiagnosticRetainsWait$")
	cmd.Env = append(os.Environ(), "RDHPF_S13_JOIN_CONTROL=1", "GORACE=atexit_sleep_ms=0")
	cmd.WaitDelay = 2 * time.Second
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "the child must diagnose its deliberately held original Wait")
	text := string(output)
	require.Contains(t, text, "S13_CLI_JOIN_DIAGNOSTIC")
	require.Contains(t, text, "S13_CLI_JOINED", "diagnostic must not abandon the original owning frame")
	require.Contains(t, text, "S13_ENV_RETIREMENT")
	require.Less(t, strings.Index(text, "S13_CLI_JOINED"), strings.Index(text, "S13_ENV_RETIREMENT"))
}
