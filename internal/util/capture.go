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
