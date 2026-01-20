package wrapper

import (
	"errors"
	"sync"
)

var ErrOutboxFull = errors.New("outbox full")

// Outbox buffers outbound messages across reconnects.
// It is optimized for "fire-and-forget" messages.
//
// IMPORTANT: For request/response protocols, buffering without correlation
// may reorder semantics. Use only for idempotent or one-way messages.
//
// Thread-safe.
type Outbox struct {
	mu sync.Mutex

	maxMessages int
	maxBytes    int

	q        [][]byte
	qBytes   int
	dropOld  bool
	closed   bool
}

type OutboxOptions struct {
	MaxMessages int  // default 4096
	MaxBytes    int  // default 4MB
	DropOldest  bool // default true; if false, Enqueue returns ErrOutboxFull
}

func NewOutbox(opts OutboxOptions) *Outbox {
	mm := opts.MaxMessages
	if mm <= 0 {
		mm = 4096
	}
	mb := opts.MaxBytes
	if mb <= 0 {
		mb = 4 << 20
	}
	return &Outbox{maxMessages: mm, maxBytes: mb, dropOld: opts.DropOldest}
}

func (o *Outbox) Close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
}

func (o *Outbox) Enqueue(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return errors.New("outbox closed")
	}
	if len(b) > o.maxBytes {
		return ErrOutboxFull
	}

	// Make room.
	for (len(o.q)+1 > o.maxMessages) || (o.qBytes+len(b) > o.maxBytes) {
		if !o.dropOld {
			return ErrOutboxFull
		}
		if len(o.q) == 0 {
			break
		}
		o.qBytes -= len(o.q[0])
		o.q[0] = nil
		o.q = o.q[1:]
	}

	cp := make([]byte, len(b))
	copy(cp, b)
	o.q = append(o.q, cp)
	o.qBytes += len(cp)
	return nil
}

func (o *Outbox) Drain(max int) (items [][]byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if max <= 0 || max > len(o.q) {
		max = len(o.q)
	}
	if max == 0 {
		return nil
	}
	items = o.q[:max]
	o.q = o.q[max:]
	for _, it := range items {
		o.qBytes -= len(it)
	}
	return items
}

func (o *Outbox) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.q)
}
