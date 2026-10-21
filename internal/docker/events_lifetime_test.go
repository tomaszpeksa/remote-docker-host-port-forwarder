package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The direct exec child transfers its own output descriptors to the test owner;
// no grandchild is left running just to keep os/exec's copiers blocked.
func TestEventReaderDescriptorChild(t *testing.T) {
	path := os.Getenv("STREAM_DESCRIPTOR_SOCKET")
	if path == "" {
		return
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		os.Exit(21)
	}
	_, _, err = conn.WriteMsgUnix([]byte(strconv.Itoa(os.Getpid())), syscall.UnixRights(1, 2), nil)
	if err != nil {
		os.Exit(22)
	}
	if os.Getenv("STREAM_WAIT_HOLD") == "1" {
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			os.Exit(23)
		}
		var request [1]byte
		if _, err := io.ReadFull(conn, request[:]); err != nil {
			os.Exit(24)
		}
		if os.Stdout.Close() != nil || os.Stderr.Close() != nil {
			os.Exit(25)
		}
		if _, err := conn.Write([]byte("R")); err != nil {
			os.Exit(26)
		}
		if _, err := io.ReadFull(conn, request[:]); err != nil && err != io.EOF {
			os.Exit(27)
		}
	}
	_ = conn.Close()
	os.Exit(0)
}

func requireStreamReaped(t *testing.T, pid int) {
	t.Helper()
	var status syscall.WaitStatus
	_, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	require.ErrorIs(t, err, syscall.ECHILD, "STREAM_TERMINAL_MUST_REAP_DIRECT_CHILD")
}

func TestEventReaderInheritedDescriptors(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "stream-fd-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	t.Setenv("PATH", dir)
	socket := filepath.Join(dir, "fds")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	defer listener.Close()
	executable, err := os.Executable()
	require.NoError(t, err)
	t.Setenv("STREAM_DESCRIPTOR_SOCKET", socket)
	t.Setenv("STREAM_TEST_EXECUTABLE", executable)
	t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\nexec \"$STREAM_TEST_EXECUTABLE\" -test.run '^TestEventReaderDescriptorChild$'\n"), 0700))
	ctx, cancel := context.WithCancel(context.Background())
	terminal := NewEventReader("ssh://owned.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil))).Stream(ctx, func(context.Context, Event) error { return nil })
	var held []*os.File
	joined := false
	defer func() {
		for _, f := range held {
			_ = f.Close()
		}
		cancel()
		if !joined {
			<-terminal
		}
	}()
	require.NoError(t, listener.SetDeadline(time.Now().Add(3*time.Second)))
	conn, err := listener.AcceptUnix()
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
	data, control := make([]byte, 32), make([]byte, syscall.CmsgSpace(8))
	n, oobn, _, _, err := conn.ReadMsgUnix(data, control)
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(data[:n]))
	require.NoError(t, err)
	messages, err := syscall.ParseSocketControlMessage(control[:oobn])
	require.NoError(t, err)
	for _, message := range messages {
		fds, err := syscall.ParseUnixRights(&message)
		require.NoError(t, err)
		for _, fd := range fds {
			held = append(held, os.NewFile(uintptr(fd), "held-stream-output"))
		}
	}
	require.Len(t, held, 2)
	select {
	case result := <-terminal:
		joined = true
		require.Error(t, result.Err, "STREAM_TERMINAL_MUST_WAIT")
		require.False(t, result.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("STREAM_INHERITED_COPY_MUST_JOIN")
	}
	requireStreamReaped(t, pid)
	for _, f := range held {
		_, err := f.Write([]byte("after-terminal"))
		require.ErrorIs(t, err, syscall.EPIPE, "STREAM_TERMINAL_MUST_CLOSE_COPY_READERS")
	}
	_, open := <-terminal
	require.False(t, open)
}

type streamCleanupHandler struct {
	slog.Handler
	entered, release, ended chan struct{}
}

func (h streamCleanupHandler) Handle(_ context.Context, r slog.Record) error {
	switch r.Message {
	case "docker events cleanup allowance exceeded; still joining owned workers":
		close(h.entered)
		<-h.release
	case "docker events stream ended":
		close(h.ended)
	}
	return nil
}

func TestEventReaderTerminalJoinsWatcher(t *testing.T) {
	installFakeSSH(t, fmt.Sprintf("{\"Action\":\"start\",\"Actor\":{\"ID\":%q}}\n", eventContainerID), "", 0)
	handler := streamCleanupHandler{Handler: slog.NewTextHandler(io.Discard, nil), entered: make(chan struct{}), release: make(chan struct{}), ended: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	delivered, releaseDelivery := make(chan struct{}), make(chan struct{})
	terminal := NewEventReader("ssh://owned.invalid", "/unused", slog.New(handler)).Stream(ctx, func(ctx context.Context, _ Event) error {
		close(delivered)
		<-releaseDelivery
		return ctx.Err()
	})
	var releaseOnce, deliveryOnce sync.Once
	joined := false
	defer func() {
		cancel()
		deliveryOnce.Do(func() { close(releaseDelivery) })
		releaseOnce.Do(func() { close(handler.release) })
		if !joined {
			<-terminal
		}
	}()
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("owned delivery did not enter")
	}
	cancel()
	select {
	case <-handler.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cleanup watcher did not observe cancellation")
	}
	deliveryOnce.Do(func() { close(releaseDelivery) })
	select {
	case <-handler.ended:
	case <-time.After(time.Second):
		t.Fatal("command and delivery did not retire")
	}
	select {
	case <-terminal:
		joined = true
		t.Error("STREAM_TERMINAL_MUST_JOIN_WATCHER")
	case <-time.After(200 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(handler.release) })
	if !joined {
		result := <-terminal
		joined = true
		require.ErrorIs(t, result.Err, context.Canceled)
		require.True(t, result.Canceled)
	}
}

func TestEventReaderNearLimitMalformedThenValid(t *testing.T) {
	hostile := strings.Repeat("x", 60<<10) + "'; touch not-an-id; #"
	valid := func(id string) string { return fmt.Sprintf("{\"Action\":\"stop\",\"Actor\":{\"ID\":%q}}\n", id) }
	installFakeSSH(t, strings.Repeat("x", (64<<10)-2)+"\n"+valid(hostile)+valid(eventContainerID), "", 0)
	var got []Event
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := <-NewEventReader("ssh://owned.invalid", "/unused", slog.New(slog.NewTextHandler(io.Discard, nil))).Stream(ctx, func(_ context.Context, event Event) error { got = append(got, event); return nil })
	require.NoError(t, result.Err)
	require.False(t, result.Canceled)
	require.EqualValues(t, 1, result.Rejected)
	require.Len(t, got, 2)
	require.Equal(t, hostile, got[0].ContainerID, "currently valid IDs are not newly restricted")
	require.Equal(t, eventContainerID, got[1].ContainerID)
	require.NoError(t, ctx.Err())
}
