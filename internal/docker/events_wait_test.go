package docker

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Only the diagnostic overlay uses started/joined. It retains the real cmd.Wait
// under this test's cleanup owner while deliberately letting the SUT return early.
// No parallel test or production command uses this test-only ownership record.
var streamWaitFault struct {
	entered, started, releaseReaper chan struct{}
	joined                          chan error
}

type streamWaitStartHandler struct {
	slog.Handler
	pid chan int
}

func (h streamWaitStartHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "docker events stream started" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "pid" {
				h.pid <- int(attr.Value.Int64())
			}
			return true
		})
	}
	return nil
}

func TestEventReaderTerminalJoinsDirectWait(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "stream-wait-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	socket := filepath.Join(dir, "fds")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	defer listener.Close()
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("PATH", dir)
	t.Setenv("STREAM_DESCRIPTOR_SOCKET", socket)
	t.Setenv("STREAM_TEST_EXECUTABLE", executable)
	t.Setenv("STREAM_WAIT_HOLD", "1")
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexec \"$STREAM_TEST_EXECUTABLE\" -test.run '^TestEventReaderDescriptorChild$'\n"), 0700))

	streamWaitFault.entered = make(chan struct{})
	streamWaitFault.started = make(chan struct{})
	streamWaitFault.joined = make(chan error)
	streamWaitFault.releaseReaper = make(chan struct{})
	var entryOnce sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	var terminal <-chan StreamResult
	var conn *net.UnixConn
	var held []*os.File
	var result StreamResult
	pid, terminalSeen := 0, false
	// Registered before Stream starts: assertion failure releases every gate and
	// descriptor, terminates the direct child, and joins both SUT and fault reaper.
	defer func() {
		entryOnce.Do(func() { close(streamWaitFault.entered) })
		if conn != nil {
			_ = conn.Close()
		}
		for _, file := range held {
			_ = file.Close()
		}
		cancel()
		close(streamWaitFault.releaseReaper)
		terminalOpen := false
		if terminal != nil {
			if !terminalSeen {
				<-terminal
			}
			_, terminalOpen = <-terminal
			t.Logf("STREAM_WAIT_SUT_JOINED pid=%d terminal_closed=true", pid)
		}
		select {
		case <-streamWaitFault.started:
			waits := 0
			for waitErr := range streamWaitFault.joined {
				waits++
				t.Logf("STREAM_WAIT_FAULT_REAPER_JOINED pid=%d real_Wait_error=%v", pid, waitErr)
			}
			require.Equal(t, 1, waits)
		default:
		}
		require.False(t, terminalOpen)
		if pid != 0 {
			requireStreamReaped(t, pid)
			t.Logf("STREAM_WAIT_CLEANUP_REAPED pid=%d ECHILD=true descriptors_closed=true", pid)
		}
	}()

	handler := streamWaitStartHandler{Handler: slog.NewTextHandler(io.Discard, nil), pid: make(chan int, 1)}
	terminal = NewEventReader("ssh://owned.invalid", "/unused", slog.New(handler)).Stream(ctx, func(context.Context, Event) error { return nil })
	require.NoError(t, listener.SetDeadline(time.Now().Add(3*time.Second)))
	conn, err = listener.AcceptUnix()
	require.NoError(t, err)
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	data, control := make([]byte, 32), make([]byte, syscall.CmsgSpace(8))
	n, oobn, _, _, err := conn.ReadMsgUnix(data, control)
	require.NoError(t, err)
	pid, err = strconv.Atoi(string(data[:n]))
	require.NoError(t, err)
	messages, err := syscall.ParseSocketControlMessage(control[:oobn])
	require.NoError(t, err)
	for _, message := range messages {
		fds, err := syscall.ParseUnixRights(&message)
		require.NoError(t, err)
		for _, fd := range fds {
			held = append(held, os.NewFile(uintptr(fd), "held-wait-output"))
		}
	}
	require.Len(t, held, 2)
	select {
	case directPID := <-handler.pid:
		require.Equal(t, directPID, pid)
	case <-time.After(time.Second):
		t.Fatal("direct command start receipt missing")
	}
	for _, file := range held {
		require.NoError(t, file.Close())
		_, err := file.Stat()
		require.ErrorIs(t, err, os.ErrClosed)
	}
	_, err = conn.Write([]byte("C"))
	require.NoError(t, err)
	var ack [1]byte
	_, err = io.ReadFull(conn, ack[:])
	require.NoError(t, err)
	require.Equal(t, byte('R'), ack[0])
	require.NoError(t, syscall.Kill(pid, 0))
	t.Logf("STREAM_WAIT_FIXTURE_ENTERED pid=%d direct_pid_matches=true child_stdio_closed=true held_descriptors_closed=2", pid)
	entryOnce.Do(func() { close(streamWaitFault.entered) })
	select {
	case result = <-terminal:
		terminalSeen = true
		t.Errorf("STREAM_TERMINAL_MUST_JOIN_DIRECT_WAIT pid=%d terminal_before_fixture_release=true", pid)
		return // Cleanup, not this assertion, releases and joins the real reaper.
	case <-time.After(200 * time.Millisecond):
	}
	_, err = conn.Write([]byte("X"))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	conn = nil
	if !terminalSeen {
		select {
		case result = <-terminal:
			terminalSeen = true
		case <-time.After(3 * time.Second):
			t.Fatal("released direct child did not finish the stream")
		}
	}
	require.NoError(t, result.Err)
	require.False(t, result.Canceled)
}
