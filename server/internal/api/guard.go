package api

import (
	"log/slog"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"vpnplatform/server/internal/config"
)

// Guard: automatic protection against request floods and abusive clients.
//
// Per client IP it keeps a one-minute request counter and a ten-minute
// "bad request" score. Over the rate limit a request gets 429; far over it
// (3x) or with too many bad requests (401/403/404/400, probes for
// .env / wp-admin / .php …) the IP is banned automatically for a while.
// Everything is in memory, bounded, and cleaned every minute.
//
// Not limited: the admin panel UI and API (the admin login has its own
// limiter) and node agents (authenticated by token); their failed auth
// still counts as bad requests, so token guessing gets banned too.

type guardEntry struct {
	minute  int64 // unix minute of the counter
	reqs    int
	window  int64 // unix 10-minute window of the bad score
	bad     int
	lastAt  int64
	bannedU int64 // banned until (unix), 0 = not banned
	reason  string
	path    string // last suspicious path, for the admin list
}

type guardState struct {
	mu      sync.Mutex
	ips     map[string]*guardEntry
	cleaned int64
}

// AutoBan is an automatic ban shown in the admin panel.
type AutoBan struct {
	IP     string    `json:"ip"`
	Until  time.Time `json:"until"`
	Reason string    `json:"reason"`
	Path   string    `json:"path"`
	Bad    int       `json:"bad"`
	Class  string    `json:"class"`
}

const guardMaxIPs = 200000

// scannerPaths are never used by the app; hitting them marks a vulnerability scanner.
var scannerHints = []string{"/.env", "/.git", "/wp-", "/wordpress", ".php", "/phpmyadmin", "/cgi-bin", "/actuator",
	"/.aws", "/vendor/", "/config.json", "/backup", "/console", "/solr", "/hnap", "/boaform", "/shell", ".asp"}

func scannerPath(p string) bool {
	l := strings.ToLower(p)
	for _, h := range scannerHints {
		if strings.Contains(l, h) {
			return true
		}
	}
	return false
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Flush / Unwrap keep streaming responses (NDJSON progress) working.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) guardAllowed(ip string, allow []string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	if a.IsLoopback() {
		return true
	}
	for _, v := range allow {
		if p, err := parseIPRule(v); err == nil && p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

func (s *Server) withGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cat().Guard
		ip := s.clientIP(r)
		path := r.URL.Path
		if cfg.Disabled || s.guardAllowed(ip, cfg.AllowIPs) {
			next.ServeHTTP(w, r)
			return
		}
		admin := strings.HasPrefix(path, "/admin")
		internal := strings.HasPrefix(path, "/internal/")
		now := time.Now().Unix()

		g := &s.guard
		g.mu.Lock()
		if g.ips == nil {
			g.ips = map[string]*guardEntry{}
		}
		if now-g.cleaned >= 60 {
			s.guardClean(now)
		}
		e := g.ips[ip]
		if e == nil {
			if len(g.ips) >= guardMaxIPs { // under a huge spread attack, keep serving rather than grow
				g.mu.Unlock()
				next.ServeHTTP(w, r)
				return
			}
			e = &guardEntry{}
			g.ips[ip] = e
		}
		e.lastAt = now
		if e.bannedU > now && !admin {
			g.mu.Unlock()
			writeErr(w, http.StatusForbidden, "ip_blocked", "访问过于频繁或存在异常，已被暂时限制，请稍后再试")
			return
		}
		limited := false
		if !admin && !internal {
			if m := now / 60; e.minute != m {
				e.minute, e.reqs = m, 0
			}
			e.reqs++
			switch {
			case e.reqs > cfg.RatePerMinute*3:
				s.guardBan(e, ip, now, cfg.BanMinutes, "请求频率异常（1 分钟超过 "+strconv.Itoa(cfg.RatePerMinute*3)+" 次）", path)
				limited = true
			case e.reqs > cfg.RatePerMinute:
				limited = true
			}
		}
		if scannerPath(path) {
			s.guardBad(e, ip, now, cfg, 10, "扫描漏洞路径", path)
		}
		g.mu.Unlock()
		if limited {
			w.Header().Set("Retry-After", "60")
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}

		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		switch rec.status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest, http.StatusMethodNotAllowed:
			if admin && rec.status != http.StatusUnauthorized {
				return // admin UI 404s etc. are not abuse
			}
			weight := 1
			if rec.status == http.StatusUnauthorized && (internal || strings.HasSuffix(path, "/login")) {
				weight = 3 // credential / token guessing
			}
			g.mu.Lock()
			if e := g.ips[ip]; e != nil {
				s.guardBad(e, ip, time.Now().Unix(), cfg, weight, "异常请求过多（认证失败 / 不存在的接口 / 格式错误）", path)
			}
			g.mu.Unlock()
		}
	})
}

// guardBad adds to the bad score; callers hold g.mu.
func (s *Server) guardBad(e *guardEntry, ip string, now int64, cfg config.GuardConfig, weight int, reason, path string) {
	if w := now / 600; e.window != w {
		e.window, e.bad = w, 0
	}
	e.bad += weight
	e.path = path
	if e.bad >= cfg.ErrorsPer10m && e.bannedU <= now {
		s.guardBan(e, ip, now, cfg.BanMinutes, reason, path)
	}
}

func (s *Server) guardBan(e *guardEntry, ip string, now int64, minutes int, reason, path string) {
	if e.bannedU > now {
		return
	}
	e.bannedU, e.reason, e.path = now+int64(minutes)*60, reason, path
	slog.Warn("guard: ip banned", "ip", ip, "minutes", minutes, "reason", reason, "path", path)
}

// guardClean drops idle entries; callers hold g.mu.
func (s *Server) guardClean(now int64) {
	g := &s.guard
	g.cleaned = now
	for ip, e := range g.ips {
		if e.bannedU <= now && now-e.lastAt > 15*60 {
			delete(g.ips, ip)
		}
	}
}

func (s *Server) autoBans() []AutoBan {
	now := time.Now().Unix()
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []AutoBan{}
	for ip, e := range g.ips {
		if e.bannedU > now {
			out = append(out, AutoBan{IP: ip, Until: time.Unix(e.bannedU, 0), Reason: e.reason, Path: e.path, Bad: e.bad})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.After(out[j].Until) })
	return out
}

func (s *Server) liftAutoBan(ip string) bool {
	g := &s.guard
	g.mu.Lock()
	defer g.mu.Unlock()
	if e := g.ips[ip]; e != nil && e.bannedU > 0 {
		e.bannedU, e.bad, e.reqs = 0, 0, 0
		return true
	}
	return false
}
