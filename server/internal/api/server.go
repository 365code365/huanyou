// Package api is the control-plane HTTP API used by the app and by node agents.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"vpnplatform/server/internal/auth"
	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/profile"
	"vpnplatform/server/internal/store"
)

type Server struct {
	cfg     *config.API
	store   *store.Store
	issuer  *auth.Issuer
	limiter *ipLimiter
}

func New(cfg *config.API, st *store.Store, issuer *auth.Issuer) *Server {
	return &Server{cfg: cfg, store: st, issuer: issuer, limiter: newIPLimiter(rate.Every(6*time.Second), 10)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("POST /api/v1/auth/register", s.rateLimited(s.handleRegister))
	mux.HandleFunc("POST /api/v1/auth/login", s.rateLimited(s.handleLogin))
	mux.HandleFunc("GET /api/v1/me", s.authed(s.handleMe))
	mux.HandleFunc("GET /api/v1/nodes", s.authed(s.handleNodes))
	mux.HandleFunc("GET /api/v1/profile", s.authed(s.handleProfile))

	mux.HandleFunc("GET /internal/v1/users", s.handleNodeUsers)
	return withRecover(mux)
}

// ---- app endpoints ----

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      userView  `json:"user"`
}

type userView struct {
	ID        int64      `json:"id"`
	Email     string     `json:"email"`
	Active    bool       `json:"active"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func viewOf(u *store.User) userView {
	return userView{ID: u.ID, Email: u.Email, Active: u.Active(time.Now()), ExpiresAt: u.ExpiresAt}
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowRegister {
		writeErr(w, http.StatusForbidden, "registration_closed", "注册已关闭")
		return
	}
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	email, ok := normalizeEmail(c.Email)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_email", "邮箱格式不正确")
		return
	}
	if len(c.Password) < 8 || len(c.Password) > 72 { // bcrypt input limit is 72 bytes
		writeErr(w, http.StatusBadRequest, "weak_password", "密码长度需为 8-72 位")
		return
	}
	hash, err := auth.HashPassword(c.Password)
	if err != nil {
		s.internal(w, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), email, hash)
	if errors.Is(err, store.ErrEmailTaken) {
		writeErr(w, http.StatusConflict, "email_taken", "该邮箱已注册")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	s.respondToken(w, u)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	email, _ := normalizeEmail(c.Email)
	u, err := s.store.UserByEmail(r.Context(), email)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, err)
		return
	}
	hash := ""
	if u != nil {
		hash = u.PasswordHash
	}
	if !auth.CheckPassword(hash, c.Password) {
		writeErr(w, http.StatusUnauthorized, "bad_credentials", "邮箱或密码错误")
		return
	}
	s.respondToken(w, u)
}

func (s *Server) respondToken(w http.ResponseWriter, u *store.User) {
	tok, exp, err := s.issuer.Issue(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{Token: tok, ExpiresAt: exp, User: viewOf(u)})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u *store.User) {
	writeJSON(w, http.StatusOK, viewOf(u))
}

type nodeView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Region string `json:"region"`
}

func (s *Server) handleNodes(w http.ResponseWriter, _ *http.Request, _ *store.User) {
	out := make([]nodeView, 0, len(s.cfg.Nodes))
	for _, n := range s.cfg.Nodes {
		out = append(out, nodeView{ID: n.ID, Name: n.Name, Region: n.Region})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !u.Active(time.Now()) {
		writeErr(w, http.StatusPaymentRequired, "inactive", "账号已过期或被停用")
		return
	}
	if len(s.cfg.Nodes) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "no_nodes", "暂无可用节点")
		return
	}
	node := &s.cfg.Nodes[0]
	if id := r.URL.Query().Get("node"); id != "" {
		node = nil
		for i := range s.cfg.Nodes {
			if s.cfg.Nodes[i].ID == id {
				node = &s.cfg.Nodes[i]
			}
		}
		if node == nil {
			writeErr(w, http.StatusNotFound, "node_not_found", "节点不存在")
			return
		}
	}
	mode := profile.ModeSmart
	if r.URL.Query().Get("mode") == string(profile.ModeGlobal) {
		mode = profile.ModeGlobal
	}
	cfgJSON, err := profile.Build(node, u.UUID, mode)
	if err != nil {
		s.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"node":   nodeView{ID: node.ID, Name: node.Name, Region: node.Region},
		"mode":   mode,
		"config": cfgJSON,
	})
}

// ---- node agent endpoint ----

type nodeUser struct {
	Email string `json:"email"`
	UUID  string `json:"uuid"`
}

func (s *Server) handleNodeUsers(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearer(r)
	if !ok || s.nodeByToken(tok) == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}
	users, err := s.store.ActiveUsers(r.Context(), time.Now())
	if err != nil {
		s.internal(w, err)
		return
	}
	out := make([]nodeUser, 0, len(users))
	for _, u := range users {
		out = append(out, nodeUser{Email: u.XrayEmail(), UUID: u.UUID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) nodeByToken(tok string) *config.Node {
	got := sha256.Sum256([]byte(tok))
	var found *config.Node
	for i := range s.cfg.Nodes {
		want := sha256.Sum256([]byte(s.cfg.Nodes[i].Token))
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			found = &s.cfg.Nodes[i]
		}
	}
	return found
}

// ---- middleware & helpers ----

func (s *Server) authed(h func(http.ResponseWriter, *http.Request, *store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearer(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "请先登录")
			return
		}
		uid, err := s.issuer.Verify(tok)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录")
			return
		}
		u, err := s.store.UserByID(r.Context(), uid)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "账号不存在")
			return
		} else if err != nil {
			s.internal(w, err)
			return
		}
		h(w, r, u)
	}
}

func (s *Server) rateLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allow(s.clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}
		h(w, r)
	}
}

func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeader {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	slog.Error("internal error", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", "服务器错误")
}

func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	return tok, ok && tok != ""
}

func normalizeEmail(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > 254 {
		return s, false
	}
	return s, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请求格式错误")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

func withRecover(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				slog.Error("panic", "path", r.URL.Path, "value", v)
				writeErr(w, http.StatusInternalServerError, "internal", "服务器错误")
			}
		}()
		h.ServeHTTP(w, r)
	})
}

// ipLimiter is a token bucket per client IP for the auth endpoints.
type ipLimiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	entries map[string]*limiterEntry
}

type limiterEntry struct {
	l    *rate.Limiter
	seen time.Time
}

func newIPLimiter(every rate.Limit, burst int) *ipLimiter {
	l := &ipLimiter{every: every, burst: burst, entries: map[string]*limiterEntry{}}
	go l.gc(context.Background())
	return l
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ip]
	if !ok {
		e = &limiterEntry{l: rate.NewLimiter(l.every, l.burst)}
		l.entries[ip] = e
	}
	e.seen = time.Now()
	return e.l.Allow()
}

func (l *ipLimiter) gc(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.mu.Lock()
			for ip, e := range l.entries {
				if time.Since(e.seen) > 30*time.Minute {
					delete(l.entries, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}
