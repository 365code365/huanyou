package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type remoteUser struct {
	Email     string `json:"email"`
	UUID      string `json:"uuid"`
	SpeedKbps int    `json:"speed_kbps"` // 0 = unlimited
}

// Syncer periodically (1) reports per-user traffic to the API and
// (2) pulls the allowed user list, applying the diff and speed limits to Xray.
type Syncer struct {
	APIURL   string
	Token    string
	Interval time.Duration
	Xray     *Xray
	Stats    *Stats
	Fair     *FairShare // optional; receives the node bandwidth from the API
	// CacheFile keeps the last user list on disk, so a node that restarts
	// while the API is unreachable still serves its users ("" = no cache).
	CacheFile string
	client    http.Client
	current   map[string]string // email -> uuid
	etag      string            // last user list version; the API answers 304 if unchanged
	blocked   string            // last applied blocked-source list (joined), to skip no-op updates
	// The API refused this node's token (deleted, token rotated) since this
	// time; zero while it is accepted.
	refusedSince time.Time
	// Traffic not yet acknowledged by the API, oldest first. A batch that
	// was sent is sent again unchanged (same id) until acknowledged, so the
	// API can tell a retry from new traffic.
	queue []trafficBatch
}

type trafficItem struct {
	Email string `json:"email"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
}

type trafficBatch struct {
	ID      string                      `json:"batch"`
	Traffic []trafficItem               `json:"traffic"`
	IPs     map[string]map[string]int64 `json:"ips,omitempty"`
	sent    bool                        // tried at least once: may already be counted, never merge
}

// maxBatchUsers bounds one report: after a long outage the backlog is cut
// into reports the API can apply well within the request timeout.
const maxBatchUsers = 2000

// refuseAfter: how long the API may refuse this node before it stops serving
// users (the API being down is not a refusal: users keep working then).
const refuseAfter = 10 * time.Minute

func (s *Syncer) Run(ctx context.Context) {
	s.client.Timeout = 15 * time.Second
	s.current = map[string]string{}
	s.loadCache(ctx)
	s.loadPending()
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	redial := time.Now()
	for {
		// A polled keep-alive connection never goes idle long enough to be
		// closed, so it would stay on the old IP forever after the API's
		// domain moves to another server. Re-dial (and re-resolve) every 5 min.
		if time.Since(redial) > 5*time.Minute {
			s.client.CloseIdleConnections()
			redial = time.Now()
		}
		if err := s.reportTraffic(ctx); err != nil {
			slog.Warn("traffic report failed", "err", err)
		}
		if err := s.syncUsers(ctx); err != nil {
			// Keep serving the last known user set; the API being down must not cut users off.
			slog.Warn("user sync failed", "err", err)
		}
		select {
		case <-ctx.Done():
			// Final flush so traffic of the last interval is not lost.
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.reportTraffic(flushCtx)
			cancel()
			s.savePending() // whatever is still unsent survives the restart
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.APIURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, b)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

func (s *Syncer) reportTraffic(ctx context.Context) error {
	if usage, ips := s.Stats.Drain(), s.Stats.DrainIPs(); len(usage) > 0 || len(ips) > 0 {
		s.enqueue(usage, ips)
	}
	for len(s.queue) > 0 {
		b := &s.queue[0]
		if !b.sent {
			// On disk as sent before it leaves: the API may count it, so after
			// a crash it must go again under the same id, never be merged into
			// a new report.
			b.sent = true
			s.savePending()
		}
		if err := s.do(ctx, http.MethodPost, "/internal/v1/traffic", b, nil); err != nil {
			return err
		}
		s.queue = s.queue[1:]
	}
	s.savePending()
	return nil
}

// enqueue adds new traffic to the queue. Batches never sent are merged with
// it (one report instead of one per failed round); a batch that was sent is
// kept as it is, since the API may already have counted it.
func (s *Syncer) enqueue(usage map[string]Usage, ips map[string]map[string]int64) {
	merged := map[string]Usage{}
	mergedIPs := map[string]map[string]int64{}
	keep := s.queue[:0]
	for _, b := range s.queue {
		if b.sent {
			keep = append(keep, b)
			continue
		}
		for _, it := range b.Traffic {
			u := merged[it.Email]
			merged[it.Email] = Usage{Up: u.Up + it.Up, Down: u.Down + it.Down}
		}
		addIPs(mergedIPs, b.IPs)
	}
	for email, u := range usage {
		m := merged[email]
		merged[email] = Usage{Up: m.Up + u.Up, Down: m.Down + u.Down}
	}
	addIPs(mergedIPs, ips)
	s.queue = keep
	var items []trafficItem
	for email, u := range merged {
		items = append(items, trafficItem{email, u.Up, u.Down})
	}
	first := true
	for len(items) > 0 || first {
		n := min(len(items), maxBatchUsers)
		b := trafficBatch{ID: newBatchID(), Traffic: items[:n]}
		if first {
			b.IPs, first = mergedIPs, false // source addresses ride with the first report
		}
		items = items[n:]
		if len(b.Traffic) > 0 || len(b.IPs) > 0 {
			s.queue = append(s.queue, b)
		}
	}
}

func addIPs(dst, src map[string]map[string]int64) {
	for email, m := range src {
		if dst[email] == nil {
			dst[email] = map[string]int64{}
		}
		for ip, n := range m {
			dst[email][ip] += n
		}
	}
}

func newBatchID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// pendingFile keeps unacknowledged traffic across restarts (next to the user cache).
func (s *Syncer) pendingFile() string {
	if s.CacheFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.CacheFile), "pending-traffic.json")
}

type pendingBatch struct {
	trafficBatch
	Sent bool `json:"sent"`
}

func (s *Syncer) savePending() {
	p := s.pendingFile()
	if p == "" {
		return
	}
	if len(s.queue) == 0 {
		_ = os.Remove(p)
		return
	}
	out := make([]pendingBatch, len(s.queue))
	for i, b := range s.queue {
		out[i] = pendingBatch{b, b.sent}
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return
	}
	tmp := p + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

func (s *Syncer) loadPending() {
	p := s.pendingFile()
	if p == "" {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var in []pendingBatch
	if json.Unmarshal(raw, &in) != nil {
		return
	}
	for _, b := range in {
		b.trafficBatch.sent = b.Sent
		s.queue = append(s.queue, b.trafficBatch)
	}
	if len(s.queue) > 0 {
		slog.Info("unsent traffic restored", "reports", len(s.queue))
	}
}

func (s *Syncer) syncUsers(ctx context.Context) error {
	var body struct {
		Users         []remoteUser `json:"users"`
		BandwidthMbps int          `json:"bandwidth_mbps"`
		BlockedIPs    []string     `json:"blocked_ips"` // risk control: drop traffic from these sources
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.APIURL, "/")+"/internal/v1/users", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	if s.etag != "" {
		req.Header.Set("If-None-Match", s.etag)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		s.refusedSince = time.Time{}
		s.touchCache()
		return nil // nothing changed since the last sync
	case http.StatusOK:
		s.refusedSince = time.Time{}
	case http.StatusUnauthorized, http.StatusForbidden:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if resp.StatusCode != http.StatusUnauthorized || e.Error != "unauthorized" {
			// Not the API refusing this node's token: its abuse guard banning
			// this address ("ip_blocked"), a WAF or proxy in front of it.
			// That passes; keep serving the current list.
			return fmt.Errorf("GET /internal/v1/users: status %d: %s", resp.StatusCode, b)
		}
		// The API is up but no longer accepts this node (deleted, token
		// rotated): after a grace period, stop admitting users instead of
		// serving the last list forever.
		if s.refusedSince.IsZero() {
			s.refusedSince = time.Now()
		}
		if time.Since(s.refusedSince) > refuseAfter && len(s.current) > 0 {
			slog.Error("API refuses this node: no users are served until it is accepted again")
			_, _ = s.apply(ctx, nil, 0, nil)
			s.etag = ""
			if s.CacheFile != "" {
				_ = os.Remove(s.CacheFile)
			}
		}
		return fmt.Errorf("GET /internal/v1/users: status %d", resp.StatusCode)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET /internal/v1/users: status %d: %s", resp.StatusCode, b)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	failed, err := s.apply(ctx, body.Users, body.BandwidthMbps, body.BlockedIPs)
	if err != nil {
		return err
	}
	// After a partly failed apply the version is not remembered, so the next
	// sync gets the full list again and retries the users that could not be
	// added. (A failed removal is not retried: Xray fails it only for a user
	// it doesn't have.)
	if failed == 0 {
		s.etag = resp.Header.Get("ETag")
	} else {
		s.etag = ""
	}
	s.saveCache(raw)
	return nil
}

// touchCache marks the cached list as current (a 304 confirms it), so its
// age check at start measures the last confirmation, not the last change.
func (s *Syncer) touchCache() {
	if s.CacheFile == "" {
		return
	}
	now := time.Now()
	_ = os.Chtimes(s.CacheFile, now, now)
}

// apply makes Xray serve exactly users (plus speed limits and blocked
// sources). failed counts users that could not be added or removed.
func (s *Syncer) apply(ctx context.Context, users []remoteUser, bandwidthMbps int, blockedIPs []string) (failed int, err error) {
	var body struct {
		Users         []remoteUser
		BandwidthMbps int
		BlockedIPs    []string
	}
	body.Users, body.BandwidthMbps, body.BlockedIPs = users, bandwidthMbps, blockedIPs
	if s.Fair != nil {
		s.Fair.SetBandwidth(body.BandwidthMbps)
	}
	if key := strings.Join(body.BlockedIPs, ","); key != s.blocked {
		if err := s.Xray.SetBlockedSources(body.BlockedIPs); err != nil {
			slog.Warn("apply blocked sources", "err", err)
		} else {
			s.blocked = key
			slog.Info("blocked sources updated", "count", len(body.BlockedIPs))
		}
	}
	want := make(map[string]remoteUser, len(body.Users))
	for _, u := range body.Users {
		want[u.Email] = u
	}

	added, removed := 0, 0
	for email, uuid := range s.current {
		if w, ok := want[email]; !ok || w.UUID != uuid {
			if err := s.Xray.RemoveUser(ctx, email); err != nil {
				slog.Warn("remove user", "err", err)
				failed++
			}
			if !ok {
				// No longer allowed: also stop the connections already open
				// (RemoveUser only refuses new ones).
				s.Stats.Revoke(email, true)
			}
			delete(s.current, email)
			removed++
		}
	}
	for email, u := range want {
		s.Stats.SetSpeed(email, u.SpeedKbps)
		if _, ok := s.current[email]; ok {
			continue
		}
		if err := s.Xray.AddUser(ctx, email, u.UUID); err != nil {
			slog.Warn("add user", "err", err)
			failed++
			continue
		}
		s.Stats.Revoke(email, false)
		s.current[email] = u.UUID
		added++
	}
	if added+removed > 0 {
		slog.Info("users synced", "added", added, "removed", removed, "total", len(s.current))
	}
	return failed, nil
}

// loadCache restores the last user list at start (before the API answers).
func (s *Syncer) loadCache(ctx context.Context) {
	if s.CacheFile == "" {
		return
	}
	if fi, err := os.Stat(s.CacheFile); err != nil || time.Since(fi.ModTime()) > 7*24*time.Hour {
		return // none, or too old to trust (plans have changed since)
	}
	raw, err := os.ReadFile(s.CacheFile)
	if err != nil {
		return
	}
	var body struct {
		Users         []remoteUser `json:"users"`
		BandwidthMbps int          `json:"bandwidth_mbps"`
		BlockedIPs    []string     `json:"blocked_ips"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	if _, err := s.apply(ctx, body.Users, body.BandwidthMbps, body.BlockedIPs); err == nil {
		slog.Info("users restored from cache", "users", len(body.Users))
	}
}

// saveCache writes the list atomically (owner-only: it holds user UUIDs).
func (s *Syncer) saveCache(raw []byte) {
	if s.CacheFile == "" {
		return
	}
	tmp := s.CacheFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, s.CacheFile)
}
