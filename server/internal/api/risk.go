package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"vpnplatform/server/internal/store"
)

// Risk control: blocked IPs / CIDRs and blocked user ids (risk_rules), and
// the per-user IP records (user_ips).
//
// Rules are read from the database into memory (refreshed every few seconds
// and right after admin edits), so checks on the request path cost a map /
// prefix lookup. IP hits are aggregated in memory and written in batches.

type riskState struct {
	mu         sync.RWMutex
	refreshing atomic.Bool // one request reloads the rules; the others use the current ones
	first      sync.Mutex  // the first load after a start: every request waits for it
	loadedAt   time.Time
	prefixes   []riskPrefix
	users      map[int64]string // user id -> reason
	nodeList   []string         // active ip rules for node agents
}

type riskPrefix struct {
	p      netip.Prefix
	reason string
}

const riskRefresh = 15 * time.Second

func (s *Server) loadRisk(ctx context.Context) error {
	rules, err := s.store.RiskRules(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	var prefixes []riskPrefix
	users := map[int64]string{}
	nodeList := []string{}
	for _, r := range rules {
		if !r.Active(now) {
			continue
		}
		switch r.Kind {
		case "ip":
			if p, err := parseIPRule(r.Value); err == nil {
				prefixes = append(prefixes, riskPrefix{p, r.Reason})
				nodeList = append(nodeList, p.String())
			}
		case "user":
			if id, err := strconv.ParseInt(r.Value, 10, 64); err == nil {
				users[id] = r.Reason
			}
		}
	}
	s.risk.mu.Lock()
	s.risk.prefixes, s.risk.users, s.risk.nodeList, s.risk.loadedAt = prefixes, users, nodeList, now
	s.risk.mu.Unlock()
	return nil
}

// riskFresh reloads rules when they are older than riskRefresh (so expiries apply).
func (s *Server) riskFresh(ctx context.Context) {
	loaded := func() time.Time {
		s.risk.mu.RLock()
		defer s.risk.mu.RUnlock()
		return s.risk.loadedAt
	}
	at := loaded()
	if time.Since(at) <= riskRefresh {
		return
	}
	if at.IsZero() {
		// Nothing loaded yet (just started): wait for the rules instead of
		// serving without any, which would let blocked accounts and
		// addresses through and send nodes an empty block list.
		s.risk.first.Lock()
		defer s.risk.first.Unlock()
		if !loaded().IsZero() {
			return
		}
		s.reloadRisk(ctx)
		return
	}
	// Only one caller reloads; every other request goes on with the rules it
	// has instead of all querying the database at once.
	if !s.risk.refreshing.CompareAndSwap(false, true) {
		return
	}
	defer s.risk.refreshing.Store(false)
	s.reloadRisk(ctx)
}

func (s *Server) reloadRisk(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.loadRisk(lctx); err != nil {
		slog.Warn("load risk rules", "err", err)
		// Keep the current rules and try again in a few seconds, not on
		// every request while the database is struggling.
		s.risk.mu.Lock()
		s.risk.loadedAt = time.Now().Add(5*time.Second - riskRefresh)
		s.risk.mu.Unlock()
	}
}

// parseIPRule accepts "1.2.3.4", "1.2.3.0/24", "2001:db8::/48".
func parseIPRule(v string) (netip.Prefix, error) {
	v = strings.TrimSpace(v)
	if strings.Contains(v, "/") {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func (s *Server) blockedIP(ctx context.Context, ip string) (string, bool) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "", false
	}
	a = a.Unmap()
	s.riskFresh(ctx)
	s.risk.mu.RLock()
	defer s.risk.mu.RUnlock()
	for _, p := range s.risk.prefixes {
		if p.p.Contains(a) {
			return p.reason, true
		}
	}
	return "", false
}

// blockedUser reports a block on the user or, for a multi-device member, its owner.
func (s *Server) blockedUser(ctx context.Context, u *store.User) (string, bool) {
	s.riskFresh(ctx)
	s.risk.mu.RLock()
	defer s.risk.mu.RUnlock()
	if r, ok := s.risk.users[u.ID]; ok {
		return r, true
	}
	if u.PrimaryID != 0 {
		if r, ok := s.risk.users[u.PrimaryID]; ok {
			return r, true
		}
	}
	return "", false
}

func (s *Server) blockedUserIDs() map[int64]bool {
	s.risk.mu.RLock()
	defer s.risk.mu.RUnlock()
	out := make(map[int64]bool, len(s.risk.users))
	for id := range s.risk.users {
		out[id] = true
	}
	return out
}

func (s *Server) nodeBlockedIPs() []string {
	s.risk.mu.RLock()
	defer s.risk.mu.RUnlock()
	return s.risk.nodeList
}

// withRiskIP rejects app API calls from blocked addresses. Admin, node agent
// and payment-callback routes are never blocked.
func (s *Server) withRiskIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			if _, blocked := s.blockedIP(r.Context(), s.clientIP(r)); blocked {
				writeErr(w, http.StatusForbidden, "ip_blocked", "当前网络已被限制访问，请联系客服")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---- IP recorder ----

type ipRecorder struct {
	mu   sync.Mutex
	hits map[ipHitKey]*store.IPHit
}

type ipHitKey struct {
	user               int64
	ip, source, nodeID string
}

const (
	ipFlushEvery = 20 * time.Second
	ipBufferMax  = 20000 // flush early above this many distinct keys
)

func (s *Server) recordIP(userID int64, ip, source, nodeID string, hits int64) {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || a.IsLoopback() || a.IsUnspecified() {
		return
	}
	cat := s.cat()
	if (source == "api" && !cat.Risk.APIOn()) || (source == "node" && !cat.Risk.NodeOn()) {
		return
	}
	ip = a.Unmap().String()
	k := ipHitKey{userID, ip, source, nodeID}
	now := time.Now().Unix()
	s.ips.mu.Lock()
	if s.ips.hits == nil {
		s.ips.hits = map[ipHitKey]*store.IPHit{}
	}
	if h, ok := s.ips.hits[k]; ok {
		h.Hits += hits
		h.At = now
	} else {
		s.ips.hits[k] = &store.IPHit{UserID: userID, IP: ip, Source: source, NodeID: nodeID, Hits: hits, At: now}
	}
	full := len(s.ips.hits) >= ipBufferMax
	s.ips.mu.Unlock()
	if full {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s.flushIPs(ctx)
		}()
	}
}

func (s *Server) flushIPs(ctx context.Context) {
	s.ips.mu.Lock()
	batch := make([]store.IPHit, 0, len(s.ips.hits))
	for _, h := range s.ips.hits {
		batch = append(batch, *h)
	}
	s.ips.hits = nil
	s.ips.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	if err := s.store.RecordIPs(ctx, batch); err != nil {
		slog.Warn("record ips", "err", err, "rows", len(batch))
	}
}

// Run does the server's background work until ctx ends: IP record flushing,
// risk rule refresh and daily clean-up. Pending IP hits are flushed on exit.
func (s *Server) Run(ctx context.Context) {
	s.riskFresh(ctx) // the first load (requests arriving meanwhile wait for it)
	flush := time.NewTicker(ipFlushEvery)
	defer flush.Stop()
	clean := time.NewTicker(6 * time.Hour)
	defer clean.Stop()
	reconcile := time.NewTicker(time.Minute)
	defer reconcile.Stop()
	tick := 0
	// Every background call has a deadline: one stuck query must not stop
	// the flushes (the event buffer drops data once full).
	within := func(d time.Duration, f func(context.Context)) {
		c, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		f(c)
	}
	go within(30*time.Minute, s.cleanup)
	var reconciling atomic.Bool
	for {
		select {
		case <-ctx.Done():
			done, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			s.flushIPs(done)
			s.flushEvents(done)
			cancel()
			return
		case <-flush.C:
			within(time.Minute, s.flushIPs)
			within(time.Minute, s.flushEvents)
		case <-clean.C:
			go within(30*time.Minute, s.cleanup)
		case <-reconcile.C:
			tick++
			// Querying payment providers can take minutes (many pending
			// orders, slow provider): it runs beside the flushes, never twice.
			if reconciling.CompareAndSwap(false, true) {
				go func(tick int) {
					defer reconciling.Store(false)
					within(10*time.Minute, func(c context.Context) { s.reconcileOrders(c, tick) })
				}(tick)
			}
		}
	}
}

func (s *Server) cleanup(ctx context.Context) {
	days := s.cat().Risk.RetentionDays
	if days <= 0 {
		days = 30
	}
	cut := time.Now().AddDate(0, 0, -days).Unix()
	if n, err := s.store.PurgeIPs(ctx, cut); err != nil {
		slog.Warn("purge ip records", "err", err)
	} else if n > 0 {
		slog.Info("purged old ip records", "rows", n, "older_than_days", days)
	}
	_ = s.store.PurgeEvents(ctx, time.Now().AddDate(0, 0, -180).Format("2006-01-02"))
	_ = s.store.PurgeCrashes(ctx, time.Now().AddDate(0, 0, -90).Unix())
	// Keep expired rules for a week so the admin can see what lapsed.
	_ = s.store.PurgeExpiredRiskRules(ctx, time.Now().AddDate(0, 0, -7).Unix())
}
