package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Only the API refusing the token counts toward giving up on it; an IP ban
// by its abuse guard (or a proxy's 403) passes and must not empty the node.
func TestSyncRefusal(t *testing.T) {
	status, code := 0, ""
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": "x"})
	}))
	defer api.Close()
	s := &Syncer{APIURL: api.URL, Token: "t"}
	for _, c := range []struct {
		status  int
		code    string
		refused bool
	}{
		{http.StatusForbidden, "ip_blocked", false},
		{http.StatusUnauthorized, "rate_limited", false},
		{http.StatusUnauthorized, "unauthorized", true},
	} {
		status, code = c.status, c.code
		s.refusedSince = time.Time{}
		if err := s.syncUsers(context.Background()); err == nil {
			t.Fatalf("%d %s: no error", c.status, c.code)
		}
		if got := !s.refusedSince.IsZero(); got != c.refused {
			t.Fatalf("%d %s: refused=%v, want %v", c.status, c.code, got, c.refused)
		}
	}
}

// A report is on disk as sent before the API can count it: after a crash it
// goes again under the same id instead of being merged into a new one.
func TestTrafficSavedAsSentBeforeSending(t *testing.T) {
	dir := t.TempDir()
	s := &Syncer{CacheFile: filepath.Join(dir, "users.json"), Token: "t", Stats: NewStats()}
	var onDisk []pendingBatch
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := os.ReadFile(s.pendingFile())
		_ = json.Unmarshal(raw, &onDisk)
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	s.APIURL = api.URL
	s.enqueue(map[string]Usage{"u1": {1, 2}}, nil)
	id := s.queue[0].ID
	if err := s.reportTraffic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != 1 || onDisk[0].ID != id || !onDisk[0].Sent {
		t.Fatalf("pending file while the API counted it: %+v", onDisk)
	}
	if _, err := os.Stat(s.pendingFile()); !os.IsNotExist(err) {
		t.Fatal("acknowledged report left on disk")
	}
}
