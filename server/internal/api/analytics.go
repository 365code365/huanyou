package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"vpnplatform/server/internal/store"
)

// App analytics and crash reports.
//
// The app batches events ({name, count}) and posts them every so often; the
// server aggregates them in memory per (day, event, visitor) and writes in
// batches. Visitors are the account ("u<id>") when a valid token is sent,
// otherwise a hash of the device id. No content, URLs or destinations are
// collected, only event names from a fixed vocabulary in the app.

var eventName = regexp.MustCompile(`^[a-z][a-z0-9_.]{1,39}$`)

type eventBuffer struct {
	mu    sync.Mutex
	hits  map[[3]string]*store.EventHit
	crash map[string]bool // crash id + visitor seen today (users count)
	day   string
}

// visitor resolves the analytics key for a request: the signed-in user, or
// a hash of a device id the app sends before it has a token.
func (s *Server) visitor(r *http.Request, deviceID string) (string, int64) {
	if tok, ok := bearer(r); ok {
		if uid, err := s.issuer.Verify(tok); err == nil {
			return "u" + strconv.FormatInt(uid, 10), uid
		}
	}
	if d := strings.TrimSpace(deviceID); len(d) >= 8 {
		h := sha256.Sum256([]byte("vpn-analytics:" + d))
		return "d" + hex.EncodeToString(h[:10]), 0
	}
	return "", 0
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func (s *Server) addEvent(day, event, vkey, platform, version string, n int64) {
	b := &s.events
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.hits == nil {
		b.hits = map[[3]string]*store.EventHit{}
	}
	k := [3]string{day, event, vkey}
	if h, ok := b.hits[k]; ok {
		h.Count += n
		if version != "" {
			h.Version = version
		}
		return
	}
	if len(b.hits) > 50000 { // flush is late (DB down); drop rather than grow
		return
	}
	b.hits[k] = &store.EventHit{Day: day, Event: event, VKey: vkey, Platform: platform, Version: version, Count: n}
}

// trackServer records an event that happens on the server (e.g. purchase_paid).
func (s *Server) trackServer(userID int64, event string) {
	s.addEvent(time.Now().Format("2006-01-02"), event, "u"+strconv.FormatInt(userID, 10), "", "", 1)
}

func (s *Server) flushEvents(ctx context.Context) {
	b := &s.events
	b.mu.Lock()
	batch := make([]store.EventHit, 0, len(b.hits))
	for _, h := range b.hits {
		batch = append(batch, *h)
	}
	b.hits = nil
	b.mu.Unlock()
	if err := s.store.RecordEvents(ctx, batch); err != nil {
		slog.Warn("record events", "err", err, "rows", len(batch))
	}
}

// handleEvents: POST /api/v1/events
//
//	{"device_id":"…","platform":"android","version":"1.2.1","events":[{"name":"app_open","count":1}]}
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"device_id"`
		Platform string `json:"platform"`
		Version  string `json:"version"`
		Events   []struct {
			Name  string `json:"name"`
			Count int64  `json:"count"`
		} `json:"events"`
	}
	if !decode(w, r, &body) {
		return
	}
	vkey, _ := s.visitor(r, body.DeviceID)
	if vkey == "" {
		writeErr(w, http.StatusBadRequest, "no_visitor", "缺少设备标识")
		return
	}
	platform := body.Platform
	if !knownPlatform(platform) {
		platform = ""
	}
	version := clip(body.Version, 32)
	day := time.Now().Format("2006-01-02")
	n := 0
	for _, e := range body.Events {
		if !eventName.MatchString(e.Name) || e.Count <= 0 {
			continue
		}
		if n++; n > 50 {
			break
		}
		s.addEvent(day, e.Name, vkey, platform, version, min(e.Count, 1000))
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCrash: POST /api/v1/crash — one crash / error report from the app.
func (s *Server) handleCrash(w http.ResponseWriter, r *http.Request) {
	var c struct {
		DeviceID  string `json:"device_id"`
		Platform  string `json:"platform"`
		Version   string `json:"version"`
		Device    string `json:"device"`
		OSVersion string `json:"os_version"`
		Kind      string `json:"kind"` // dart | flutter | native | vpn
		Message   string `json:"message"`
		Stack     string `json:"stack"`
	}
	if !decode(w, r, &c) {
		return
	}
	vkey, uid := s.visitor(r, c.DeviceID)
	if vkey == "" || strings.TrimSpace(c.Message) == "" {
		writeErr(w, http.StatusBadRequest, "bad_report", "报告格式错误")
		return
	}
	kind := c.Kind
	switch kind {
	case "dart", "flutter", "native", "vpn":
	default:
		kind = "dart"
	}
	rep := store.Crash{
		UserID: uid, Platform: clip(c.Platform, 16), AppVersion: clip(c.Version, 32), Device: clip(c.Device, 120),
		OSVersion: clip(c.OSVersion, 60), Kind: kind, Message: clip(c.Message, 1500), Stack: clip(c.Stack, 16000),
	}
	rep.ID = store.CrashID(rep.AppVersion, kind, rep.Message, rep.Stack)
	b := &s.events
	b.mu.Lock()
	day := time.Now().Format("2006-01-02")
	if b.day != day || b.crash == nil {
		b.day, b.crash = day, map[string]bool{}
	}
	seen := b.crash[rep.ID+vkey]
	if len(b.crash) < 100000 {
		b.crash[rep.ID+vkey] = true
	}
	b.mu.Unlock()
	if err := s.store.RecordCrash(r.Context(), rep, !seen); err != nil {
		s.internal(w, err)
		return
	}
	s.addEvent(day, "crash", vkey, rep.Platform, rep.AppVersion, 1)
	w.WriteHeader(http.StatusNoContent)
}

// ---- admin ----

func (s *Server) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 30
	}
	ctx := r.Context()
	s.flushEvents(ctx) // include the last few seconds
	now := time.Now()
	daily, err := s.store.DailyStats(ctx, now, days)
	if err != nil {
		s.internal(w, err)
		return
	}
	since := func(n int) string { return now.AddDate(0, 0, -(n - 1)).Format("2006-01-02") }
	uv7, _ := s.store.UniqueVisitors(ctx, since(7))
	uv30, _ := s.store.UniqueVisitors(ctx, since(30))
	events, err := s.store.EventStats(ctx, since(7))
	if err != nil {
		s.internal(w, err)
		return
	}
	platforms, _ := s.store.OpenBreakdown(ctx, since(7), "platform")
	versions, _ := s.store.OpenBreakdown(ctx, since(7), "version")
	groups, reports, _ := s.store.CrashCount(ctx, now.AddDate(0, 0, -7).Unix())
	today := daily[len(daily)-1]
	writeJSON(w, http.StatusOK, map[string]any{
		"daily": daily, "today_uv": today.UV, "uv_7": uv7, "uv_30": uv30,
		"events": events, "platforms": platforms, "versions": versions,
		"crash_groups_7": groups, "crash_reports_7": reports,
	})
}

func (s *Server) adminCrashes(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Crashes(r.Context(), r.URL.Query().Get("all") == "1", 200)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) adminCrash(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.CrashByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) adminResolveCrash(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Resolved bool `json:"resolved"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.store.SetCrashResolved(r.Context(), r.PathValue("id"), in.Resolved); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
