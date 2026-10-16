package util

import "errors"

// ErrOutputLimit means an observation was not captured completely.
var ErrOutputLimit = errors.New("command output limit exceeded")

// LimitedBuffer retains a bounded prefix. Overflow is an error, never success
// with truncated data. A single command's stdout copier owns Write.
type LimitedBuffer struct {
	data     []byte
	exceeded bool
}

func NewLimitedBuffer(limit int) *LimitedBuffer { return &LimitedBuffer{data: make([]byte, 0, limit)} }
func (b *LimitedBuffer) Write(p []byte) (int, error) {
	remaining := cap(b.data) - len(b.data)
	if len(p) > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.exceeded = true
		return remaining, ErrOutputLimit
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *LimitedBuffer) Bytes() []byte { return b.data }
func (b *LimitedBuffer) Err() error {
	if b.exceeded {
		return ErrOutputLimit
	}
	return nil
}

// TailBuffer drains diagnostics while retaining only their bounded tail.
// Read it only after the owning writer has joined.
type TailBuffer struct {
	data      []byte
	truncated bool
}

func NewTailBuffer(limit int) *TailBuffer { return &TailBuffer{data: make([]byte, 0, limit)} }
func (b *TailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	limit := cap(b.data)
	if len(b.data)+n > limit {
		b.truncated = true
	}
	if n >= limit {
		b.data = b.data[:limit]
		copy(b.data, p[n-limit:])
		return n, nil
	}
	if excess := len(b.data) + n - limit; excess > 0 {
		copy(b.data, b.data[excess:])
		b.data = b.data[:len(b.data)-excess]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *TailBuffer) String() string  { return string(b.data) }
func (b *TailBuffer) Truncated() bool { return b.truncated }
