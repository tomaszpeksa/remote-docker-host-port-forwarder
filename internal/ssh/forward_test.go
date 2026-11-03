package ssh

import (
	"context"
	"encoding/binary"
	"errors"
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
)

func forwardTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func quietForwardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestForwardPositiveRepliesAndExactTuple(t *testing.T) {
	master, dir := masterFixture(t)
	calls := filepath.Join(dir, "forwards")
	t.Setenv("S12_MUX_RECORD", calls)
	require.NoError(t, master.Open(forwardTestContext(t)))
	var retained *Forwarding
	require.NoError(t, master.WithForwarding(forwardTestContext(t), func(f *Forwarding) error {
		retained = f
		effect, err := AddForward(context.Background(), f, 15432, 5432, quietForwardLogger())
		require.NoError(t, err)
		require.Equal(t, ForwardAcknowledged, effect)
		effect, err = AddForward(context.Background(), f, 15432, 5432, quietForwardLogger())
		require.NoError(t, err)
		require.Equal(t, ForwardAcknowledged, effect, "duplicate OPEN_OK is not exclusive creation evidence")
		effect, err = CancelForward(context.Background(), f, 15432, 5433, quietForwardLogger())
		require.Error(t, err)
		require.Equal(t, ForwardRejected, effect, "wrong tuple is not cancellation")
		effect, err = CancelForward(context.Background(), f, 15432, 5432, quietForwardLogger())
		require.NoError(t, err)
		require.Equal(t, ForwardAcknowledged, effect)
		effect, err = CancelForward(context.Background(), f, 15432, 5432, quietForwardLogger())
		require.Error(t, err)
		require.Equal(t, ForwardRejected, effect, "absent tuple does not discharge older work")
		return nil
	}))
	effect, err := retained.Open(context.Background(), 15432, 5432)
	require.Error(t, err)
	require.Equal(t, ForwardNotDispatched, effect, "expired capability must fail closed")
	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Equal(t, "OPEN 15432 5432\nOPEN 15432 5432\nCLOSE 15432 5433\nCLOSE 15432 5432\nCLOSE 15432 5432\n", string(data))
}

func TestAddForwardWithRetryBackoff(t *testing.T) {
	master, dir := masterFixture(t)
	failure := filepath.Join(dir, "fail")
	t.Setenv("S12_MUX_OPEN_FAIL", failure)
	require.NoError(t, os.WriteFile(failure, []byte("address already in use"), 0600))
	require.NoError(t, master.Open(forwardTestContext(t)))
	require.NoError(t, master.WithForwarding(forwardTestContext(t), func(f *Forwarding) error {
		var waits []time.Duration
		after := func(d time.Duration) <-chan time.Time {
			waits = append(waits, d)
			ready := make(chan time.Time, 1)
			ready <- time.Time{}
			return ready
		}
		effect, err := addForwardWithRetry(context.Background(), f, 15432, 5432, quietForwardLogger(), after)
		require.Equal(t, ForwardRejected, effect)
		require.ErrorIs(t, err, ErrPortInUse)
		require.Equal(t, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond}, waits)
		require.NoError(t, os.Remove(failure))
		effect, err = AddForwardWithRetry(context.Background(), f, 15432, 5432, quietForwardLogger())
		require.NoError(t, err)
		require.Equal(t, ForwardAcknowledged, effect)
		return nil
	}))
}

func TestAddForwardWithRetryCanceledDuringBackoff(t *testing.T) {
	master, dir := masterFixture(t)
	failure := filepath.Join(dir, "fail")
	t.Setenv("S12_MUX_OPEN_FAIL", failure)
	require.NoError(t, os.WriteFile(failure, []byte("address already in use"), 0600))
	require.NoError(t, master.Open(forwardTestContext(t)))
	require.NoError(t, master.WithForwarding(forwardTestContext(t), func(f *Forwarding) error {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		count := 0
		effect, err := addForwardWithRetry(ctx, f, 15432, 5432, quietForwardLogger(), func(d time.Duration) <-chan time.Time {
			count++
			require.Equal(t, 100*time.Millisecond, d)
			cancel()
			return make(chan time.Time)
		})
		require.Equal(t, ForwardRejected, effect)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, 1, count)
		return nil
	}))
}

func TestCalculateBackoff(t *testing.T) {
	for i, want := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 6400 * time.Millisecond, 10 * time.Second, 10 * time.Second} {
		require.Equal(t, want, calculateBackoff(i))
	}
}
func TestIsAddressInUse(t *testing.T) {
	require.False(t, isAddressInUse(nil))
	require.True(t, isAddressInUse(errors.New("address already in use")))
	require.False(t, isAddressInUse(errors.New("Port forwarding failed")), "generic rejection is not bind-conflict proof")
}

// These fixtures speak literal frames, independent of the production encoder.
func wireFrame(words ...uint32) []byte {
	frame := make([]byte, 4+4*len(words))
	binary.BigEndian.PutUint32(frame, uint32(4*len(words)))
	for i, value := range words {
		binary.BigEndian.PutUint32(frame[4+4*i:], value)
	}
	return frame
}
func readWireFrame(conn net.Conn) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size > 65536 {
		return nil, fmt.Errorf("fixture oversized frame")
	}
	data := make([]byte, int(size))
	_, err := io.ReadFull(conn, data)
	return data, err
}
func TestForwardMalformedAndLostReplies(t *testing.T) {
	for _, tc := range []struct {
		name         string
		hello, reply []byte
		want         ForwardEffect
	}{
		{"wrong-version", wireFrame(1, 5), nil, ForwardNotDispatched},
		{"truncated-hello-extension", wireFrame(1, 4, 1), nil, ForwardNotDispatched},
		{"trailing-ok-data", wireFrame(1, 4), wireFrame(0x80000001, 1, 0), ForwardUncertain},
		{"malformed-rejection", wireFrame(1, 4), wireFrame(0x80000003, 1, 100), ForwardUncertain},
		{"permission-rejected", wireFrame(1, 4), wireFrame(0x80000002, 1, 0), ForwardRejected},
		{"wrong-id", wireFrame(1, 4), wireFrame(0x80000001, 2), ForwardUncertain},
		{"wrong-type", wireFrame(1, 4), wireFrame(0x80000099, 1), ForwardUncertain},
		{"truncated", wireFrame(1, 4), []byte{0, 0, 0, 8, 0x80}, ForwardUncertain},
		{"oversized", wireFrame(1, 4), []byte{0, 2, 0, 0}, ForwardUncertain},
		{"lost", wireFrame(1, 4), nil, ForwardUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				_, _ = readWireFrame(server)
				_, _ = server.Write(tc.hello)
				if tc.want == ForwardNotDispatched {
					return
				}
				_, _ = readWireFrame(server)
				if tc.reply != nil {
					_, _ = server.Write(tc.reply)
				}
			}()
			effect, err := exchangeForward(forwardTestContext(t), client, muxOpenForward, 15432, 5432)
			require.Error(t, err)
			require.Equal(t, tc.want, effect)
			<-done
		})
	}
}

type partialForwardConn struct {
	net.Conn
	requestBytes int
	writes       int
}

func (c *partialForwardConn) Write(data []byte) (int, error) {
	c.writes++
	if c.writes != 2 {
		return c.Conn.Write(data)
	}
	if c.requestBytes > 0 {
		n, err := c.Conn.Write(data[:c.requestBytes])
		if err != nil {
			return n, err
		}
	}
	return c.requestBytes, io.ErrClosedPipe
}
func TestMuxFrameRangeGuards(t *testing.T) {
	for _, size := range []int{0, muxForwardFrameLimit + 1, muxForwardFrameLimit} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			n, err := writeMuxFrame(io.Discard, make([]byte, size))
			if size < 4 || size > muxForwardFrameLimit {
				require.Error(t, err, "MUX_OUTBOUND_FRAME_MUST_BE_PROTOCOL_BOUNDED")
				require.Zero(t, n)
			} else {
				require.NoError(t, err)
				require.Equal(t, size+4, n)
			}
		})
	}
	for _, body := range [][]byte{{0, 0, 0}, {0, 0, 0, 1}, {0, 1, 0, 1}} {
		_, _, err := muxString(body)
		require.Error(t, err)
	}
}

func TestForwardPartialDispatchEffect(t *testing.T) {
	for _, written := range []int{0, 3, 12} {
		t.Run(fmt.Sprint(written), func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				_, _ = readWireFrame(server)
				_, _ = server.Write(wireFrame(1, 4))
				if written > 0 {
					_, _ = io.ReadFull(server, make([]byte, written))
				}
			}()
			effect, err := exchangeForward(forwardTestContext(t), &partialForwardConn{Conn: client, requestBytes: written}, muxOpenForward, 15432, 5432)
			require.ErrorIs(t, err, io.ErrClosedPipe)
			want := ForwardUncertain
			if written == 0 {
				want = ForwardNotDispatched
			}
			require.Equal(t, want, effect)
			<-done
		})
	}
}
func TestForwardUnresponsiveReplyBound(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		_, _ = readWireFrame(server)
		_, _ = server.Write(wireFrame(1, 4))
		_, _ = readWireFrame(server)
		_, _ = io.Copy(io.Discard, server)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	effect, err := exchangeForward(ctx, client, muxOpenForward, 15432, 5432)
	require.Error(t, err)
	require.Equal(t, ForwardUncertain, effect)
	require.Less(t, time.Since(started), time.Second)
	require.NoError(t, client.Close())
	<-done
}

func TestForwardCancellationJoinsIO(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		_, _ = readWireFrame(server)
		_, _ = server.Write(wireFrame(1, 4))
		_, _ = readWireFrame(server)
		cancel()
		_, _ = io.Copy(io.Discard, server)
	}()
	effect, err := exchangeForward(ctx, client, muxCloseForward, 15432, 5432)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, ForwardUncertain, effect)
	require.NoError(t, client.Close())
	<-done
}

func TestForwardNoFallbackOnMissingOrForeignPeer(t *testing.T) {
	master, dir := masterFixture(t)
	t.Setenv("S12_MUX_RECORD", filepath.Join(dir, "forwards"))
	require.NoError(t, master.Open(forwardTestContext(t)))
	require.NoError(t, master.WithForwarding(forwardTestContext(t), func(f *Forwarding) error {
		original := master.controlPath
		defer func() { master.controlPath = original }()
		master.controlPath = filepath.Join(dir, "missing")
		effect, err := f.Open(context.Background(), 15432, 5432)
		require.Error(t, err)
		require.Equal(t, ForwardNotDispatched, effect)
		foreign, err := net.Listen("unix", master.controlPath)
		require.NoError(t, err)
		defer foreign.Close()
		effect, err = f.Open(context.Background(), 15432, 5432)
		require.ErrorContains(t, err, "does not belong")
		require.Equal(t, ForwardNotDispatched, effect)
		return nil
	}))
	require.NoFileExists(t, filepath.Join(dir, "forwards"))
}

func TestForwardRejectionDoesNotRetryUnknown(t *testing.T) {
	master, dir := masterFixture(t)
	record, lost := filepath.Join(dir, "calls"), filepath.Join(dir, "lost")
	t.Setenv("S12_MUX_RECORD", record)
	t.Setenv("S12_MUX_LOST_REPLY", lost)
	require.NoError(t, os.WriteFile(lost, nil, 0600))
	require.NoError(t, master.Open(forwardTestContext(t)))
	require.NoError(t, master.WithForwarding(forwardTestContext(t), func(f *Forwarding) error {
		effect, err := AddForwardWithRetry(context.Background(), f, 15432, 5432, quietForwardLogger())
		require.Error(t, err)
		require.Equal(t, ForwardUncertain, effect)
		return nil
	}))
	data, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(data), "OPEN"))
}
