package ssh

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func forwardTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func quietForwardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestAddForwardArgv(t *testing.T) {
	for _, tc := range []struct {
		name, host string
		want       []string
	}{
		{"default_port", "ssh://docker@fixture.invalid", []string{"-o", "BatchMode=yes", "-S", "/control socket", "-O", "forward", "-L", "127.0.0.1:15432:localhost:5432", "--", "docker@fixture.invalid"}},
		{"explicit_port", "ssh://user@fixture.invalid:2222", []string{"-o", "BatchMode=yes", "-S", "/control socket", "-p", "2222", "-O", "forward", "-L", "127.0.0.1:15432:localhost:5432", "--", "user@fixture.invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := installFakeSSH(t, sshResponse{})
			require.NoError(t, AddForward(forwardTestContext(t), "/control socket", tc.host, 15432, 5432, quietForwardLogger()))
			require.Equal(t, [][]string{tc.want}, fake.calls(t))
		})
	}
}

func requirePortConflict(t *testing.T, err error, output string) {
	t.Helper()
	var conflict *PortConflictError
	require.ErrorAs(t, err, &conflict)
	require.Same(t, conflict, err, "return the conflict itself, not an extra wrapper")
	require.Equal(t, 15432, conflict.Port)
	require.Empty(t, conflict.ContainerID, "AddForward does not populate container identity")
	require.Equal(t, output, conflict.Output)
	require.EqualError(t, err, "port 15432 already in use locally")
	require.Same(t, ErrPortInUse, conflict.Unwrap())
	require.ErrorIs(t, err, ErrPortInUse)
	wrapped := fmt.Errorf("caller context: %w", err)
	var recovered *PortConflictError
	require.ErrorAs(t, wrapped, &recovered)
	require.Same(t, conflict, recovered)
	require.ErrorIs(t, wrapped, ErrPortInUse)
	require.NotErrorIs(t, err, &PortConflictError{Port: 15432, Output: output})
}

func TestAddForwardClassification(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		conflict     bool
	}{
		{"lowercase", "address already in use", true},
		{"uppercase", "Address already in use", true},
		{"cannot_listen", "cannot listen to port: 15432", true},
		{"remote_forward", "remote port forwarding failed", true},
		{"bind_address", "bind: Address already in use", true},
		{"bind_bracket", "bind [127.0.0.1]:15432: fixture failure", true},
		{"unrelated", "permission denied", false},
	} {
		for _, stream := range []string{"stdout", "stderr"} {
			t.Run(tc.name+"/"+stream, func(t *testing.T) {
				response := sshResponse{status: 7}
				if stream == "stdout" {
					response.stdout = "  " + tc.output + "\n"
				} else {
					response.stderr = "  " + tc.output + "\n"
				}
				fake := installFakeSSH(t, response)
				err := AddForward(forwardTestContext(t), "/control socket", "ssh://docker@fixture.invalid", 15432, 5432, quietForwardLogger())
				if tc.conflict {
					requirePortConflict(t, err, tc.output)
				} else {
					require.EqualError(t, err, "failed to add port forward 15432->5432: exit status 7 (output: permission denied)")
					require.NotErrorIs(t, err, ErrPortInUse)
					var exitErr *exec.ExitError
					require.ErrorAs(t, err, &exitErr)
					require.Equal(t, 7, exitErr.ExitCode())
				}
				require.Len(t, fake.calls(t), 1)
			})
		}
	}
}

type forwardRetryCase struct {
	name      string
	responses []sshResponse
	calls     int
	output    string
	unrelated bool
}

func forwardRetryCases() []forwardRetryCase {
	return []forwardRetryCase{
		{name: "immediate_success", responses: []sshResponse{{}}, calls: 1},
		{name: "conflicts_then_success", responses: []sshResponse{
			{stderr: "address already in use: first", status: 7},
			{stdout: "bind [127.0.0.1]:15432: second", status: 7},
			{},
		}, calls: 3},
		{name: "unrelated_failure", responses: []sshResponse{{stderr: "permission denied", status: 7}}, calls: 1, unrelated: true},
		{name: "five_attempt_exhaustion", responses: []sshResponse{
			{stderr: "address already in use: first", status: 7},
			{stderr: "address already in use: second", status: 7},
			{stderr: "address already in use: third", status: 7},
			{stderr: "address already in use: fourth", status: 7},
			{stderr: "  address already in use: fifth\n", status: 7},
		}, calls: 5, output: "address already in use: fifth"},
	}
}

func requireRetryResult(t *testing.T, tc forwardRetryCase, err error, fake *fakeSSH) {
	t.Helper()
	if tc.output != "" {
		requirePortConflict(t, err, tc.output)
	} else if tc.unrelated {
		require.EqualError(t, err, "failed to add port forward 15432->5432: exit status 7 (output: permission denied)")
		require.NotErrorIs(t, err, ErrPortInUse)
	} else {
		require.NoError(t, err)
	}
	calls := fake.calls(t)
	require.Len(t, calls, tc.calls)
	for _, argv := range calls {
		require.Equal(t, []string{"-o", "BatchMode=yes", "-S", "/control socket", "-p", "2222", "-O", "forward", "-L", "127.0.0.1:15432:localhost:5432", "--", "docker@fixture.invalid"}, argv)
	}
}

// Exercise the exported wrapper as well as the controlled timer path: real
// subprocess results must reach the retry loop and real AddForward on every call.
func TestAddForwardWithRetry(t *testing.T) {
	for _, tc := range forwardRetryCases() {
		t.Run(tc.name, func(t *testing.T) {
			fake := installFakeSSH(t, tc.responses...)
			err := AddForwardWithRetry(forwardTestContext(t), "/control socket", "ssh://docker@fixture.invalid:2222", 15432, 5432, quietForwardLogger())
			requireRetryResult(t, tc, err, fake)
		})
	}
}

func forwardLogRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	decoder := json.NewDecoder(buf)
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if err == io.EOF {
			return records
		}
		require.NoError(t, err)
		delete(record, "time")
		records = append(records, record)
	}
}

func TestAddForwardLogging(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict_%t", conflict), func(t *testing.T) {
			response := sshResponse{}
			if conflict {
				response = sshResponse{stderr: "address already in use", status: 7}
			}
			installFakeSSH(t, response)
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			err := AddForward(forwardTestContext(t), "/control socket", "ssh://docker@fixture.invalid", 15432, 5432, logger)
			want := []map[string]any{{"level": "INFO", "msg": "adding SSH port forward", "localPort": float64(15432), "remotePort": float64(5432), "host": "docker@fixture.invalid"}}
			if conflict {
				require.ErrorIs(t, err, ErrPortInUse)
				want = append(want, map[string]any{"level": "WARN", "msg": "port already in use locally", "port": float64(15432), "remotePort": float64(5432), "suggestion": "Check what's using the port with: lsof -i :15432", "note": "Other forwards will continue normally"})
			} else {
				require.NoError(t, err)
				want = append(want, map[string]any{"level": "INFO", "msg": "SSH port forward added successfully", "localPort": float64(15432), "remotePort": float64(5432)})
			}
			require.Equal(t, want, forwardLogRecords(t, &logs))
		})
	}
}

func TestCalculateBackoff(t *testing.T) {
	for attempt, want := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 6400 * time.Millisecond, 10 * time.Second, 10 * time.Second} {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			require.Equal(t, want, calculateBackoff(attempt))
		})
	}
	require.Equal(t, 10*time.Second, calculateBackoff(20))
}

func TestIsAddressInUse(t *testing.T) {
	require.False(t, isAddressInUse(nil))
	for _, message := range []string{"address already in use", "Address already in use", "cannot listen to port", "remote port forwarding failed", "bind: Address already in use", "bind ["} {
		t.Run(message, func(t *testing.T) {
			require.True(t, isAddressInUse(errors.New("prefix: "+message+": suffix")))
		})
	}
	for _, message := range []string{"", "connection refused", "permission denied", "network unreachable", "some other error"} {
		t.Run(message, func(t *testing.T) {
			require.False(t, isAddressInUse(errors.New(message)))
		})
	}
}

func TestAddForwardWithRetryBackoff(t *testing.T) {
	for _, tc := range forwardRetryCases() {
		t.Run(tc.name, func(t *testing.T) {
			fake := installFakeSSH(t, tc.responses...)
			var waits []time.Duration
			after := func(delay time.Duration) <-chan time.Time {
				waits = append(waits, delay)
				// Each wait follows a completed command, including the first wait.
				require.Len(t, fake.calls(t), len(waits))
				ready := make(chan time.Time, 1)
				ready <- time.Time{}
				return ready
			}
			err := addForwardWithRetry(forwardTestContext(t), "/control socket", "ssh://docker@fixture.invalid:2222", 15432, 5432, quietForwardLogger(), after)
			requireRetryResult(t, tc, err, fake)
			want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}
			if tc.calls == 1 {
				require.Empty(t, waits)
			} else {
				require.Equal(t, want[:tc.calls-1], waits)
			}
		})
	}
}

func TestAddForwardWithRetryCanceledDuringBackoff(t *testing.T) {
	fake := installFakeSSH(t, sshResponse{stderr: "address already in use", status: 7}, sshResponse{})
	ctx, cancel := context.WithCancel(forwardTestContext(t))
	defer cancel()
	waits := 0
	after := func(delay time.Duration) <-chan time.Time {
		waits++
		require.Equal(t, 100*time.Millisecond, delay)
		require.Len(t, fake.calls(t), 1)
		// The backoff is reached only after the owned process has been reaped.
		// Leave its timer pending so only cancellation can release the select.
		cancel()
		return make(chan time.Time)
	}
	err := addForwardWithRetry(ctx, "/control socket", "ssh://docker@fixture.invalid:2222", 15432, 5432, quietForwardLogger(), after)
	require.Same(t, context.Canceled, err)
	require.Same(t, ctx.Err(), err)
	require.Equal(t, 1, waits)
	require.Len(t, fake.calls(t), 1, "cancellation must not start another SSH command")
}

func TestAddForwardWithRetryLogging(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprintf("exhausted_%t", exhausted), func(t *testing.T) {
			responses := []sshResponse{{stderr: "address already in use", status: 7}, {}}
			if exhausted {
				responses = forwardRetryCases()[3].responses
			}
			installFakeSSH(t, responses...)
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			after := func(time.Duration) <-chan time.Time {
				ready := make(chan time.Time, 1)
				ready <- time.Time{}
				return ready
			}
			err := addForwardWithRetry(forwardTestContext(t), "/control socket", "ssh://docker@fixture.invalid", 15432, 5432, logger, after)
			if exhausted {
				require.ErrorIs(t, err, ErrPortInUse)
			} else {
				require.NoError(t, err)
			}
			var retryLogs []map[string]any
			for _, record := range forwardLogRecords(t, &logs) {
				switch record["msg"] {
				case "port conflict detected, will retry", "retrying port forward after backoff", "port forward succeeded after retry", "port forward failed after max retries":
					retryLogs = append(retryLogs, record)
				}
			}
			var want []map[string]any
			for i, delay := range []string{"100ms", "200ms", "400ms", "800ms"}[:len(responses)-1] {
				want = append(want,
					map[string]any{"level": "DEBUG", "msg": "port conflict detected, will retry", "port": float64(15432), "attempt": float64(i + 1), "maxAttempts": float64(5)},
					map[string]any{"level": "DEBUG", "msg": "retrying port forward after backoff", "port": float64(15432), "attempt": float64(i + 2), "maxAttempts": float64(5), "delay": delay})
			}
			if exhausted {
				want = append(want, map[string]any{"level": "WARN", "msg": "port forward failed after max retries", "port": float64(15432), "maxAttempts": float64(5), "error": "port 15432 already in use locally"})
			} else {
				want = append(want, map[string]any{"level": "INFO", "msg": "port forward succeeded after retry", "port": float64(15432), "attempts": float64(2)})
			}
			require.Equal(t, want, retryLogs)
		})
	}
}
