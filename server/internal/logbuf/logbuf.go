// Package logbuf keeps the most recent log records in memory so the admin
// panel can show the server console live (and hand a slice of it to an AI for
// analysis). It wraps the process's slog handler: every record still goes to
// stderr (journald) as before.
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one log line.
type Entry struct {
	ID    int64     `json:"id"`
	Time  time.Time `json:"time"`
	Level string    `json:"level"` // DEBUG | INFO | WARN | ERROR
	Msg   string    `json:"msg"`
	Attrs string    `json:"attrs"` // key=value pairs, as in the text log
}

// Buffer is a fixed-size ring of entries.
type Buffer struct {
	mu   sync.Mutex
	ring []Entry
	next int64 // ID of the next entry
	size int
	subs map[chan struct{}]struct{}
}

// Default is the process buffer (set by cmd/api); nil-safe to use.
var Default *Buffer

func New(size int) *Buffer {
	return &Buffer{ring: make([]Entry, 0, size), size: size, next: 1, subs: map[chan struct{}]struct{}{}}
}

func (b *Buffer) add(e Entry) {
	b.mu.Lock()
	e.ID = b.next
	b.next++
	if len(b.ring) < b.size {
		b.ring = append(b.ring, e)
	} else {
		b.ring[(e.ID-1)%int64(b.size)] = e
	}
	for c := range b.subs {
		select {
		case c <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
}

// Filter selects entries.
type Filter struct {
	After    int64  // only IDs > After
	MinLevel string // "", INFO, WARN, ERROR
	Query    string // case-insensitive substring of msg + attrs
	Limit    int    // newest N (default 500)
}

func levelRank(l string) int {
	switch l {
	case "DEBUG":
		return 0
	case "WARN":
		return 2
	case "ERROR":
		return 3
	default:
		return 1
	}
}

// List returns matching entries, oldest first, and the last ID in the buffer.
func (b *Buffer) List(f Filter) ([]Entry, int64) {
	if b == nil {
		return nil, 0
	}
	if f.Limit <= 0 || f.Limit > b.size {
		f.Limit = 500
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	min := levelRank(f.MinLevel)
	if f.MinLevel == "" {
		min = 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(b.ring)
	out := make([]Entry, 0, min2(n, f.Limit))
	// Walk newest to oldest, then reverse.
	for i := 0; i < n && len(out) < f.Limit; i++ {
		id := b.next - 1 - int64(i)
		e := b.ring[(id-1)%int64(b.size)]
		if e.ID <= f.After {
			break
		}
		if levelRank(e.Level) < min {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Msg+" "+e.Attrs), q) {
			continue
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, b.next - 1
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Wait blocks until a new entry arrives, ctx ends or d passes.
func (b *Buffer) Wait(ctx context.Context, after int64, d time.Duration) {
	if b == nil {
		return
	}
	c := make(chan struct{}, 1)
	b.mu.Lock()
	if b.next-1 > after {
		b.mu.Unlock()
		return
	}
	b.subs[c] = struct{}{}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, c)
		b.mu.Unlock()
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-c:
	case <-ctx.Done():
	case <-t.C:
	}
}

// Wrap returns a handler that writes to next and records into b.
func (b *Buffer) Wrap(next slog.Handler) slog.Handler { return &handler{b: b, next: next} }

type handler struct {
	b      *Buffer
	next   slog.Handler
	prefix string // attrs from WithAttrs / WithGroup, already formatted
	group  string
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(h.prefix)
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&sb, h.group, a)
		return true
	})
	t := r.Time
	if t.IsZero() {
		t = time.Now()
	}
	h.b.add(Entry{Time: t, Level: r.Level.String(), Msg: r.Message, Attrs: strings.TrimSpace(sb.String())})
	return h.next.Handle(ctx, r)
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	var sb strings.Builder
	sb.WriteString(h.prefix)
	for _, a := range as {
		writeAttr(&sb, h.group, a)
	}
	return &handler{b: h.b, next: h.next.WithAttrs(as), prefix: sb.String(), group: h.group}
}

func (h *handler) WithGroup(name string) slog.Handler {
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &handler{b: h.b, next: h.next.WithGroup(name), prefix: h.prefix, group: g}
}

func writeAttr(sb *strings.Builder, group string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if group != "" {
		key = group + "." + key
	}
	if a.Value.Kind() == slog.KindGroup {
		for _, g := range a.Value.Group() {
			writeAttr(sb, key, g)
		}
		return
	}
	v := a.Value.String()
	if strings.ContainsAny(v, " \t\"=") {
		v = fmt.Sprintf("%q", v)
	}
	sb.WriteString(" " + key + "=" + v)
}
