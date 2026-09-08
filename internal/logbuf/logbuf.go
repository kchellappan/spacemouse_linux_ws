// Package logbuf keeps the most recent log records in memory so the status UI
// can show them without reading the journal.
//
// A user diagnosing a broken setup is usually in a browser, not a terminal,
// and often cannot reach journalctl at all under a flatpak or snap browser.
// The records the bridge already emits are the best evidence available; this
// makes them reachable.
package logbuf

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Record is one log line, flattened for JSON.
type Record struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
	// Seq orders records and lets a client resume without duplicates.
	Seq uint64 `json:"seq"`
}

// Buffer is a fixed-size ring of the most recent records.
//
// Writes never block and never fail: logging is on the path of the WAMP read
// loop and the drive loop, and a status page must not be able to stall
// navigation. When the ring is full the oldest record is overwritten.
type Buffer struct {
	mu   sync.RWMutex
	ring []Record
	next int
	seq  uint64

	// subs are woken on each write. Each holds a buffered channel of one and
	// coalesces: a slow reader sees the latest state, never a backlog.
	subs map[chan struct{}]struct{}
}

// New returns a buffer holding at most size records.
func New(size int) *Buffer {
	if size <= 0 {
		size = 500
	}
	return &Buffer{
		ring: make([]Record, size),
		subs: map[chan struct{}]struct{}{},
	}
}

// Append stores a record and wakes subscribers.
func (b *Buffer) Append(r Record) {
	b.mu.Lock()
	b.seq++
	r.Seq = b.seq
	b.ring[b.next] = r
	b.next = (b.next + 1) % len(b.ring)
	subs := make([]chan struct{}, 0, len(b.subs))
	for c := range b.subs {
		subs = append(subs, c)
	}
	b.mu.Unlock()

	for _, c := range subs {
		select {
		case c <- struct{}{}:
		default: // already pending; the reader will see this write too
		}
	}
}

// Since returns records newer than seq, oldest first. Passing 0 returns
// everything the ring still holds.
func (b *Buffer) Since(seq uint64) []Record {
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]Record, 0, len(b.ring))
	// Walk from the oldest slot so the result is already in order.
	for i := 0; i < len(b.ring); i++ {
		r := b.ring[(b.next+i)%len(b.ring)]
		if r.Seq > seq {
			out = append(out, r)
		}
	}
	return out
}

// Subscribe returns a channel woken on each write, and a function to release
// it. The channel is buffered and coalescing, so a reader that falls behind
// misses wake-ups but never records: it reads them with Since.
func (b *Buffer) Subscribe() (<-chan struct{}, func()) {
	c := make(chan struct{}, 1)
	b.mu.Lock()
	b.subs[c] = struct{}{}
	b.mu.Unlock()

	return c, func() {
		b.mu.Lock()
		delete(b.subs, c)
		b.mu.Unlock()
	}
}

// Handler tees records into the buffer while passing them to next, so the
// journal keeps its copy.
type Handler struct {
	next slog.Handler
	buf  *Buffer
	// attrs and groups accumulate from WithAttrs and WithGroup, which slog
	// expects a handler to carry rather than apply immediately.
	attrs []slog.Attr
}

// NewHandler wraps next so every record it sees also lands in buf.
func NewHandler(next slog.Handler, buf *Buffer) *Handler {
	return &Handler{next: next, buf: buf}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	attrs := map[string]string{}
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	if len(attrs) == 0 {
		attrs = nil
	}

	h.buf.Append(Record{
		Time:    r.Time,
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   attrs,
	})
	return h.next.Handle(ctx, r)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &Handler{next: h.next.WithAttrs(attrs), buf: h.buf, attrs: merged}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name), buf: h.buf, attrs: h.attrs}
}
