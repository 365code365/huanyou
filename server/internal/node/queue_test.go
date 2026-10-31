package node

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestTrafficQueue(t *testing.T) {
	s := &Syncer{CacheFile: filepath.Join(t.TempDir(), "users.json")}
	s.enqueue(map[string]Usage{"u1": {10, 20}}, map[string]map[string]int64{"u1": {"203.0.113.1": 1}})
	s.queue[0].sent = true // tried, no answer: must go out again exactly as it was
	first := s.queue[0].ID
	s.enqueue(map[string]Usage{"u1": {1, 1}}, nil)
	s.enqueue(map[string]Usage{"u2": {5, 5}}, nil) // merged with the unsent one before it
	if len(s.queue) != 2 || s.queue[0].ID != first || s.queue[0].Traffic[0].Up != 10 {
		t.Fatalf("sent batch changed: %+v", s.queue)
	}
	got := map[string]Usage{}
	for _, it := range s.queue[1].Traffic {
		got[it.Email] = Usage{it.Up, it.Down}
	}
	if got["u1"] != (Usage{1, 1}) || got["u2"] != (Usage{5, 5}) {
		t.Fatalf("unsent batches not merged: %+v", got)
	}

	// A big backlog is cut into reports of at most maxBatchUsers.
	big := map[string]Usage{}
	for i := 0; i < maxBatchUsers*2+5; i++ {
		big[fmt.Sprintf("u%d", i+100)] = Usage{1, 1}
	}
	s2 := &Syncer{}
	s2.enqueue(big, nil)
	if len(s2.queue) != 3 || len(s2.queue[0].Traffic) != maxBatchUsers || len(s2.queue[2].Traffic) != 5 {
		t.Fatalf("chunks: %d", len(s2.queue))
	}

	// Unsent traffic survives a restart, with its sent flag.
	s.savePending()
	r := &Syncer{CacheFile: s.CacheFile}
	r.loadPending()
	if len(r.queue) != 2 || r.queue[0].ID != first || !r.queue[0].sent || r.queue[1].sent {
		t.Fatalf("restored: %+v", r.queue)
	}
	s.queue = nil
	s.savePending()
	r2 := &Syncer{CacheFile: s.CacheFile}
	r2.loadPending()
	if len(r2.queue) != 0 {
		t.Fatal("acknowledged traffic restored")
	}
}
