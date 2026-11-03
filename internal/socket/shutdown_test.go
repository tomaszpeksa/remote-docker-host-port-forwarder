package socket

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tomaszpeksa/remote-docker-host-port-forwarder/internal/state"
)

// Observe the real accepted Unix connection without replacing its I/O.
type shutdownListener struct {
	net.Listener
	writing, finished, callbackDone chan struct{}
}

func (l *shutdownListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &shutdownConnection{Conn: conn, writing: l.writing, finished: l.finished, callbackDone: l.callbackDone}, nil
}

type shutdownConnection struct {
	net.Conn
	writing, finished, callbackDone chan struct{}
	writeOnce, closeOnce            sync.Once
}

func (c *shutdownConnection) Write(p []byte) (int, error) {
	c.writeOnce.Do(func() { close(c.writing) })
	return c.Conn.Write(p)
}
func (c *shutdownConnection) Close() error {
	err := c.Conn.Close()
	select {
	case <-c.callbackDone:
		c.closeOnce.Do(func() { close(c.finished) })
	default:
	}
	return err
}

type shutdownLogHandler struct{ entered, release, done chan struct{} }

func (h *shutdownLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *shutdownLogHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "failed to write snapshot to socket" {
		close(h.entered)
		<-h.release
		close(h.done)
	}
	return nil
}
func (h *shutdownLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *shutdownLogHandler) WithGroup(string) slog.Handler      { return h }

func TestServerCloseJoinsInterruptedHandlers(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s13-socket-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	logs := &shutdownLogHandler{entered: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{})}
	// This exceeds the Unix send buffer; the peer deliberately never reads.
	server, err := NewServerAtPath(strings.Repeat("h", 2<<20), dir+"/status.sock", state.NewState(), state.NewHistory(), time.Now(), slog.New(logs))
	require.NoError(t, err)
	observed := &shutdownListener{Listener: server.listener, writing: make(chan struct{}), finished: make(chan struct{}), callbackDone: logs.done}
	server.listener = observed
	ctx, cancel := context.WithCancel(context.Background())
	started, closed := make(chan error, 1), make(chan error, 1)
	go func() { started <- server.Start(ctx) }()
	client, err := net.Dial("unix", server.socketPath)
	if err != nil {
		cancel()
		_ = server.Close()
		<-started
		t.Fatal(err)
	}
	// Release and join are installed before any assertion on injected stalls.
	t.Cleanup(func() {
		close(logs.release)
		_ = client.Close()
		cancel()
		require.NoError(t, <-closed)
		require.NoError(t, <-started)
		<-observed.finished
	})
	<-observed.writing
	cancel()
	go func() { closed <- server.Close() }()
	select {
	case <-logs.entered:
	case err := <-closed:
		closed <- err
		t.Fatal("Close returned before interrupting and joining the accepted handler")
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not interrupt the stalled accepted socket")
	}
	select {
	case err := <-closed:
		closed <- err
		t.Fatal("Close returned while the accepted handler still owns its callback")
	case <-time.After(100 * time.Millisecond):
	}
	_, err = os.Lstat(server.socketPath)
	require.NoError(t, err, "path must remain owned until handler completion")
}

type heldAcceptListener struct {
	net.Listener
	entered, release chan struct{}
}

func (l *heldAcceptListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		close(l.entered)
		<-l.release
	}
	return conn, err
}

func TestServerCloseJoinsAcceptedConnectionBeforeRegistration(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s13-accept-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	server, err := NewServerAtPath("ssh://owned", dir+"/status.sock", state.NewState(), state.NewHistory(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	held := &heldAcceptListener{Listener: server.listener, entered: make(chan struct{}), release: make(chan struct{})}
	server.listener = held
	started, closed := make(chan error, 1), make(chan error, 1)
	go func() { started <- server.Start(context.Background()) }()
	client, err := net.Dial("unix", server.socketPath)
	if err != nil {
		_ = server.Close()
		<-started
		t.Fatal(err)
	}
	var once sync.Once
	release := func() { once.Do(func() { close(held.release) }) }
	t.Cleanup(func() { release(); _ = client.Close(); require.NoError(t, <-closed); require.NoError(t, <-started) })
	<-held.entered
	go func() { closed <- server.Close() }()
	select {
	case err := <-closed:
		closed <- err
		t.Fatal("Close retired its path before the accept owner joined the unregistered connection")
	case <-time.After(100 * time.Millisecond):
	}
	_, err = os.Lstat(server.socketPath)
	require.NoError(t, err)
	release()
}

type closeErrorListener struct {
	net.Listener
	closed chan struct{}
}

func (l *closeErrorListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &closeErrorConnection{Conn: conn, closed: l.closed}, nil
}

type closeErrorConnection struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *closeErrorConnection) Close() error {
	_ = c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return io.ErrUnexpectedEOF
}

func TestServerRetainsAcceptedConnectionCleanupError(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s13-close-error-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	server, err := NewServerAtPath("ssh://owned", dir+"/status.sock", state.NewState(), state.NewHistory(), time.Now(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	listener := &closeErrorListener{Listener: server.listener, closed: make(chan struct{})}
	server.listener = listener
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- server.Start(ctx) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); require.NoError(t, <-started) })
	client, err := net.Dial("unix", server.socketPath)
	require.NoError(t, err)
	defer client.Close()
	<-listener.closed
	require.ErrorIs(t, server.Close(), io.ErrUnexpectedEOF, "accepted connection cleanup errors must survive shutdown")
}
