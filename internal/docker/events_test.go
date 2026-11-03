package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const eventContainerID = "abc123def4567890123456789012345678901234567890123456789012345678"

// collectStream drains both channels before returning, so Stream has called Wait.
// Cancel also releases Stream's context-watcher goroutine after normal EOF.
func collectStream(t *testing.T, host, controlPath string, logger *slog.Logger) ([]Event, []error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []Event
	result := <-NewEventReader(host, controlPath, logger).Stream(ctx, func(ctx context.Context, event Event) error { got = append(got, event); return nil })
	var failures []error
	if result.Err != nil {
		failures = append(failures, result.Err)
	}
	require.NoError(t, ctx.Err(), "fixture stream did not complete before deadline")
	return got, failures
}

func TestEventReaderStreamRejectsShortID(t *testing.T) {
	if os.Getenv("RDHPF_SHORT_ID_CHILD") == "1" {
		got, errs := collectStream(t, "ssh://owned-fixture.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.Empty(t, errs)
		require.Len(t, got, 1)
		require.Equal(t, eventContainerID, got[0].ContainerID)
		return
	}
	installFakeSSH(t, fmt.Sprintf("{\"Action\":\"start\",\"Actor\":{\"ID\":\"x\"}}\n{\"Action\":\"stop\"}\n{\"Action\":\"start\",\"Actor\":{\"ID\":%q}}\n", eventContainerID), "", 0)
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestEventReaderStreamRejectsShortID$")
	cmd.Env = append(os.Environ(), "RDHPF_SHORT_ID_CHILD=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err())
	require.NoError(t, err, "MALFORMED_RECORD_MUST_NOT_PANIC: %s", output)
}

func TestEventReaderStreamDiagnosticLimit(t *testing.T) {
	installFakeSSH(t, "", "", 0)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte(`#!/bin/sh
i=0
while [ "$i" -lt 100 ]; do printf '%1000s' '' >&2; i=$((i+1)); done
printf tail >&2
`), 0700))
	var logs streamLogBuffer
	_, errs := collectStream(t, "ssh://owned-fixture.invalid", "/unused", slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	require.Empty(t, errs)
	for _, record := range logs.records(t) {
		if stderr, ok := record["stderr"].(string); ok {
			require.LessOrEqual(t, len(stderr), 64<<10, "STREAM_DIAGNOSTICS_BOUNDED")
			require.True(t, strings.HasSuffix(stderr, "tail"))
		}
	}
}

func TestEventReaderCompletionOwnsWorkers(t *testing.T) {
	installFakeSSH(t, "", "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := <-NewEventReader("ssh://owned-fixture.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil))).Stream(ctx, func(context.Context, Event) error { return nil })
	require.NoError(t, result.Err)
	var stacks string
	require.Eventually(t, func() bool {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		stacks = string(buf[:n])
		return !strings.Contains(stacks, "docker.(*EventReader).")
	}, time.Second, time.Millisecond, "STREAM_TERMINAL_MUST_JOIN_OWNED_WORKERS: %s", stacks)
}

func TestEventReaderOwnedCancellation(t *testing.T) {
	installFakeSSH(t, "", "", 0)
	// More than pipe capacity while delivery is held: cancellation must unblock
	// the scanner's pipe writer and join os/exec's stdout/stderr copiers.
	line := fmt.Sprintf("{\"Action\":\"start\",\"Actor\":{\"ID\":%q}}", eventContainerID)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte("#!/bin/sh\ni=0; while [ \"$i\" -lt 10000 ]; do printf '%s\\n' '"+line+"'; i=$((i+1)); done\n"), 0700))
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	var logs streamLogBuffer
	done := NewEventReader("ssh://owned-fixture.invalid", "/unused", slog.New(slog.NewJSONHandler(&logs, nil))).Stream(ctx, func(ctx context.Context, e Event) error { close(entered); <-ctx.Done(); return ctx.Err() })
	joined := false
	defer func() {
		cancel()
		if !joined {
			<-done
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("owned delivery did not enter")
	}
	cancel()
	result := <-done
	joined = true
	require.True(t, result.Canceled)
	require.ErrorIs(t, result.Err, context.Canceled)
	pid := 0
	for _, record := range logs.records(t) {
		if record["msg"] == "docker events stream started" {
			pid = int(record["pid"].(float64))
		}
	}
	require.Positive(t, pid)
	requireStreamReaped(t, pid)
	_, ok := <-done
	require.False(t, ok, "one terminal result")
}

func TestEventReaderOversizedOpenStderr(t *testing.T) {
	installFakeSSH(t, "", "", 0)
	require.NoError(t, os.WriteFile(filepath.Join(os.Getenv("PATH"), "ssh"), []byte(`#!/bin/sh
i=0
while [ "$i" -lt 100 ]; do printf '%1000s' ''; i=$((i+1)); done
while :; do :; done
`), 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := <-NewEventReader("ssh://owned-fixture.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil))).Stream(ctx, func(context.Context, Event) error { return nil })
	require.NoError(t, ctx.Err(), "SCANNER_ERROR_MUST_NOT_WAIT_FOR_OPEN_STDERR")
	require.ErrorContains(t, result.Err, "scanner error")
	require.False(t, result.Canceled)
}

func TestEventReaderStreamParsing(t *testing.T) {
	cases := []struct {
		name, fields, want string
	}{
		{"start", `"Action":"start"`, "start"},
		{"die", `"Action":"die"`, "die"},
		{"stop", `"Action":"stop"`, "stop"},
		{"action_precedence", `"Action":"start","status":"die"`, "start"},
		{"empty_action_falls_back", `"Action":"","status":"die"`, "die"},
		{"absent_action_falls_back", `"status":"stop"`, "stop"},
		{"ignored_action_does_not_fall_back", `"Action":"create","status":"start"`, ""},
		{"ignored_action", `"Action":"destroy"`, ""},
		{"unknown_structure", `"unknown":"field"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := fmt.Sprintf(`{%s,"Actor":{"ID":%q},"time":1699564800,"timeNano":1699564800123456789}`, tc.fields, eventContainerID)
			installFakeSSH(t, line+"\n", "", 0)
			got, errs := collectStream(t, "ssh://docker@fixture.invalid", "/control socket", slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.Empty(t, errs)
			if tc.want == "" {
				require.Empty(t, got)
			} else {
				require.Equal(t, []Event{{Type: tc.want, ContainerID: eventContainerID, Timestamp: time.Unix(1699564800, 0)}}, got)
			}
		})
	}
}

// Stream's cancellation watcher may log after the data channels close.
type streamLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *streamLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *streamLogBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var records []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b.buf.Bytes()))
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if err == io.EOF {
			return records
		}
		require.NoError(t, err)
		records = append(records, record)
	}
}

func TestEventReaderStreamMalformedThenValid(t *testing.T) {
	for _, malformed := range []string{`{"Action":`, "not JSON", ""} {
		t.Run(fmt.Sprintf("line_%q", malformed), func(t *testing.T) {
			valid := fmt.Sprintf(`{"Action":"start","Actor":{"ID":%q},"time":42}`, eventContainerID)
			installFakeSSH(t, malformed+"\n"+valid+"\n", "", 0)
			var logs streamLogBuffer
			got, errs := collectStream(t, "ssh://docker@fixture.invalid", "/control socket", slog.New(slog.NewJSONHandler(&logs, nil)))
			require.Empty(t, errs)
			require.Equal(t, []Event{{Type: "start", ContainerID: eventContainerID, Timestamp: time.Unix(42, 0)}}, got)
			var warnings []map[string]any
			for _, record := range logs.records(t) {
				if record["msg"] == "failed to parse docker event JSON" {
					warnings = append(warnings, record)
				}
			}
			require.Len(t, warnings, 1)
			require.Equal(t, "WARN", warnings[0]["level"])
			require.Equal(t, malformed, warnings[0]["line"])
			require.NotEmpty(t, warnings[0]["error"])
		})
	}
}

func TestEventReaderStreamArgvAndLogging(t *testing.T) {
	for _, tc := range []struct {
		name, host, destination, port string
		want                          []string
	}{
		{"default_port", "ssh://docker@fixture.invalid", "docker@fixture.invalid", "", []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", "/control socket", "--", "docker@fixture.invalid"}},
		{"explicit_port", "ssh://user@fixture.invalid:2222", "user@fixture.invalid", "2222", []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", "/control socket", "-p", "2222", "--", "user@fixture.invalid"}},
		{"ipv4", "ssh://root@192.0.2.1", "root@192.0.2.1", "", []string{"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "Tunnel=no", "-S", "/control socket", "--", "root@192.0.2.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := installFakeSSH(t, "", "", 0)
			var logs streamLogBuffer
			got, errs := collectStream(t, tc.host, "/control socket", slog.New(slog.NewJSONHandler(&logs, nil)))
			require.Empty(t, got)
			require.Empty(t, errs)
			want := append(tc.want, `sh -c "docker events --format '{{json .}}' --filter type=container --filter event=start --filter event=die --filter event=stop"`)
			require.Equal(t, want, argv())
			var commandLogs []map[string]any
			for _, record := range logs.records(t) {
				if record["msg"] == "executing docker events command via shell" {
					commandLogs = append(commandLogs, record)
				}
			}
			require.Len(t, commandLogs, 1)
			record := commandLogs[0]
			require.Equal(t, "INFO", record["level"])
			require.Equal(t, tc.destination, record["host"])
			require.Equal(t, tc.port, record["port"])
			require.Equal(t, `docker events --format '{{json .}}' --filter type=container --filter event=start --filter event=die --filter event=stop`, record["dockerCmd"])
			require.Equal(t, fmt.Sprint(want), record["args"])
		})
	}
}

func TestEventReaderStreamCommandFailure(t *testing.T) {
	installFakeSSH(t, "", "fixture failure\n", 7)
	var logs streamLogBuffer
	got, errs := collectStream(t, "ssh://docker@fixture.invalid", "/control socket", slog.New(slog.NewJSONHandler(&logs, nil)))
	require.Empty(t, got)
	require.Len(t, errs, 1)
	require.True(t, strings.Contains(errs[0].Error(), "docker events command failed: exit status 7"))
	var failures []map[string]any
	for _, record := range logs.records(t) {
		if record["msg"] == "docker events command failed" {
			failures = append(failures, record)
		}
	}
	require.Len(t, failures, 1)
	require.Equal(t, float64(7), failures[0]["exitCode"])
	require.Equal(t, "fixture failure\n", failures[0]["stderr"])
}

func TestEventReaderCanceledBeforeStart(t *testing.T) {
	installFakeSSH(t, "", "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := NewEventReader("ssh://fixture.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil)))
	result := <-reader.Stream(ctx, func(context.Context, Event) error { return nil })
	require.True(t, result.Canceled, "PRESTART_CANCELLATION_IS_TERMINAL")
	require.ErrorIs(t, result.Err, context.Canceled)
}
