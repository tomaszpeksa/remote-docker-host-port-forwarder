package ssh

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ForwardEffect describes only this request, never ownership or older work.
type ForwardEffect uint8

const (
	ForwardNotDispatched ForwardEffect = iota
	ForwardRejected
	ForwardAcknowledged
	ForwardUncertain
)

const (
	muxHello             = 0x00000001
	muxOpenForward       = 0x10000006
	muxCloseForward      = 0x10000007
	muxOK                = 0x80000001
	muxPermissionDenied  = 0x80000002
	muxFailure           = 0x80000003
	muxLocalForward      = 1
	muxVersion           = 4
	muxForwardFrameLimit = 64 << 10
)

// Forwarding is a concrete capability valid only inside WithForwarding. It must
// not be retained or used to re-enter Master. Retirement waits for its operations.
// OPEN acknowledgements can be idempotent: the caller's sole-producer ledger,
// not this capability or a reply, establishes application tuple ownership.
type Forwarding struct {
	mu         sync.Mutex
	master     *Master
	generation uint64
	ready      bool
	active     bool
}

func (f *Forwarding) Generation() uint64 { return f.generation }
func (f *Forwarding) Ready() bool        { return f.ready }

// WithForwarding holds the existing lifecycle gate for the complete transaction.
func (m *Master) WithForwarding(ctx context.Context, operation func(*Forwarding) error) error {
	return m.WithConnection(ctx, func(generation uint64, ready bool) error {
		f := &Forwarding{master: m, generation: generation, ready: ready, active: true}
		defer func() { f.mu.Lock(); f.active = false; f.mu.Unlock() }()
		return operation(f)
	})
}

// Open requests one local TCP forward; it never starts an ordinary SSH client.
func (f *Forwarding) Open(ctx context.Context, localPort, remotePort int) (ForwardEffect, error) {
	return f.request(ctx, muxOpenForward, localPort, remotePort)
}

// Close requests removal of one exact local TCP forward. Rejection is not absence.
func (f *Forwarding) Close(ctx context.Context, localPort, remotePort int) (ForwardEffect, error) {
	return f.request(ctx, muxCloseForward, localPort, remotePort)
}

func (f *Forwarding) request(ctx context.Context, operation uint32, localPort, remotePort int) (effect ForwardEffect, err error) {
	if f == nil {
		return ForwardNotDispatched, fmt.Errorf("no forwarding capability")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.active || !f.ready {
		return ForwardNotDispatched, fmt.Errorf("forwarding capability unavailable")
	}
	if localPort < 1 || localPort > 65535 || remotePort < 1 || remotePort > 65535 {
		return ForwardNotDispatched, fmt.Errorf("invalid TCP forwarding port")
	}
	bounded, cancel := context.WithTimeout(ctx, masterCheckTimeout)
	defer cancel()
	conn, err := f.master.associatedConnection(bounded)
	if err != nil {
		return ForwardNotDispatched, err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	return exchangeForward(bounded, conn, operation, localPort, remotePort)
}

// exchangeForward uses the already verified FD for HELLO and exactly one request.
// Cancellation interrupts I/O and its callback is joined before returning the FD.
func exchangeForward(ctx context.Context, conn net.Conn, operation uint32, localPort, remotePort int) (effect ForwardEffect, err error) {
	if localPort < 1 || localPort > 65535 || remotePort < 1 || remotePort > 65535 {
		return ForwardNotDispatched, fmt.Errorf("invalid local TCP forwarding port")
	}
	if err := ctx.Err(); err != nil {
		return ForwardNotDispatched, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return ForwardNotDispatched, err
		}
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()); close(interrupted) })
	defer func() {
		if !stop() {
			<-interrupted
		}
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
	}()
	if _, err := writeMuxFrame(conn, muxWords(muxHello, muxVersion)); err != nil {
		return ForwardNotDispatched, err
	}
	hello, err := readMuxFrame(conn)
	if err != nil {
		return ForwardNotDispatched, err
	}
	if len(hello) < 8 || binary.BigEndian.Uint32(hello) != muxHello || binary.BigEndian.Uint32(hello[4:]) != muxVersion {
		return ForwardNotDispatched, fmt.Errorf("unsupported mux hello")
	}
	// Extensions are string pairs. Ignore their meaning, not malformed framing.
	for rest := hello[8:]; len(rest) > 0; {
		_, next, err := muxString(rest)
		if err != nil {
			return ForwardNotDispatched, err
		}
		rest = next
		_, next, err = muxString(rest)
		if err != nil {
			return ForwardNotDispatched, err
		}
		rest = next
	}
	if err := ctx.Err(); err != nil {
		return ForwardNotDispatched, err
	}
	const requestID = 1 // one request per freshly associated connection
	request := muxWords(operation, requestID, muxLocalForward)
	request = append(request, muxWords(uint32(len("127.0.0.1")))...)
	request = append(request, "127.0.0.1"...)
	request = append(request, muxWords(uint32(localPort))...)
	request = append(request, muxWords(uint32(len("localhost")))...)
	request = append(request, "localhost"...)
	request = append(request, muxWords(uint32(remotePort))...)
	written, err := writeMuxFrame(conn, request)
	if err != nil {
		if written == 0 {
			return ForwardNotDispatched, err
		}
		return ForwardUncertain, err
	}
	reply, err := readMuxFrame(conn)
	if err != nil {
		return ForwardUncertain, err
	}
	if len(reply) < 8 || binary.BigEndian.Uint32(reply[4:]) != requestID {
		return ForwardUncertain, fmt.Errorf("invalid mux forwarding reply ID")
	}
	switch binary.BigEndian.Uint32(reply) {
	case muxOK:
		if len(reply) != 8 {
			return ForwardUncertain, fmt.Errorf("invalid mux OK reply")
		}
		return ForwardAcknowledged, nil
	case muxFailure, muxPermissionDenied:
		message, rest, err := muxString(reply[8:])
		if err != nil || len(rest) != 0 {
			return ForwardUncertain, fmt.Errorf("invalid mux rejection reply")
		}
		return ForwardRejected, fmt.Errorf("mux forwarding rejected: %s", message)
	default:
		return ForwardUncertain, fmt.Errorf("unexpected mux forwarding reply")
	}
}

func muxWords(values ...uint32) []byte {
	b := make([]byte, len(values)*4)
	for i, v := range values {
		binary.BigEndian.PutUint32(b[i*4:], v)
	}
	return b
}
func muxString(b []byte) (string, []byte, error) {
	if len(b) < 4 {
		return "", nil, fmt.Errorf("truncated mux string")
	}
	size := binary.BigEndian.Uint32(b)
	if size > muxForwardFrameLimit {
		return "", nil, fmt.Errorf("truncated mux string")
	}
	length := int(size)
	if length > len(b)-4 {
		return "", nil, fmt.Errorf("truncated mux string")
	}
	return string(b[4 : 4+length]), b[4+length:], nil
}
func writeMuxFrame(w io.Writer, payload []byte) (int, error) {
	size := len(payload)
	if size < 4 || size > muxForwardFrameLimit {
		return 0, fmt.Errorf("invalid outbound mux frame size")
	}
	frame := append(muxWords(uint32(size)), payload...)
	total := 0
	for len(frame) > 0 {
		n, err := w.Write(frame)
		total += n
		frame = frame[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}
func readMuxFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size < 4 || size > muxForwardFrameLimit {
		return nil, fmt.Errorf("invalid mux frame size")
	}
	body := make([]byte, int(size))
	_, err := io.ReadFull(r, body)
	return body, err
}
