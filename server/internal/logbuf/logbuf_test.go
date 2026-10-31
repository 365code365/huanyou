package logbuf

import (
	"io"
	"log/slog"
	"testing"
)

func TestBufferRingAndFilter(t *testing.T) {
	b := New(3)
	l := slog.New(b.Wrap(slog.NewTextHandler(io.Discard, nil))).With("svc", "api")
	l.Info("one")
	l.Warn("two", "order", "VP1")
	l.Error("three", "err", "boom happened")
	l.Info("four")
	all, last := b.List(Filter{})
	if last != 4 || len(all) != 3 || all[0].Msg != "two" || all[2].Msg != "four" {
		t.Fatalf("ring: %d %+v", last, all)
	}
	if all[0].Attrs != "svc=api order=VP1" || all[1].Attrs != `svc=api err="boom happened"` {
		t.Fatalf("attrs: %q %q", all[0].Attrs, all[1].Attrs)
	}
	if w, _ := b.List(Filter{MinLevel: "WARN"}); len(w) != 2 {
		t.Fatalf("level: %+v", w)
	}
	if q, _ := b.List(Filter{Query: "vp1"}); len(q) != 1 || q[0].ID != 2 {
		t.Fatalf("query: %+v", q)
	}
	if a, _ := b.List(Filter{After: 3}); len(a) != 1 || a[0].Msg != "four" {
		t.Fatalf("after: %+v", a)
	}
}
