// Package api is the control-plane HTTP API used by the app and by node agents.
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	"vpnplatform/server/internal/monitor"
	"vpnplatform/server/internal/payment"
	"vpnplatform/server/internal/profile"
	"vpnplatform/server/internal/store"
)

type Server struct {
	cfg       *config.API         // static settings from api.json
	live      *config.LiveCatalog // admin-editable catalog (plans, nodes, apps, settings)
	store     *store.Store
	issuer    *auth.Issuer
	limiter   *ipLimiter
	providers []payment.Provider
	mock      *payment.Mock
	monitor   *monitor.Monitor
	nodeUsers nodeUserCache
}

func New(cfg *config.API, live *config.LiveCatalog, st *store.Store, issuer *auth.Issuer, mon *monitor.Monitor) (*Server, error) {
	providers, mock, err := newProviders(cfg)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, live: live, store: st, issuer: issuer, limiter: newIPLimiter(rate.Every(6*time.Second), 10),
		providers: providers, mock: mock, monitor: mon}, nil
}

// cat returns the current catalog snapshot (read-only).
func (s *Server) cat() *config.Catalog { return s.live.Get() }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("POST /api/v1/auth/device", s.rateLimited(s.handleDevice))
	mux.HandleFunc("POST /api/v1/auth/register", s.rateLimited(s.handleRegister))
	mux.HandleFunc("POST /api/v1/auth/login", s.rateLimited(s.handleLogin))
	mux.HandleFunc("GET /api/v1/me", s.authed(s.handleMe))
	mux.HandleFunc("GET /api/v1/nodes", s.authed(s.handleNodes))
	mux.HandleFunc("GET /api/v1/profile", s.authed(s.handleProfile))
	mux.HandleFunc("GET /api/v1/usage", s.authed(s.handleUsage))
	mux.HandleFunc("GET /api/v1/referral", s.authed(s.handleReferral))
	mux.HandleFunc("POST /api/v1/referral/bind", s.authed(s.handleReferralBind))
	mux.HandleFunc("GET /api/v1/contacts", s.handleContacts)
	mux.HandleFunc("GET /api/v1/devices", s.authed(s.handleDevices))
	mux.HandleFunc("POST /api/v1/devices/code", s.authed(s.handleDeviceCode))
	mux.HandleFunc("POST /api/v1/devices/join", s.authed(s.handleDeviceJoin))
	mux.HandleFunc("POST /api/v1/devices/leave", s.authed(s.handleDeviceLeave))
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.authed(s.handleDeviceRemove))
	mux.HandleFunc("GET /l/{code}", s.handleLinkLanding)
	mux.HandleFunc("GET /i/{code}", s.handleLanding)
	mux.HandleFunc("GET /download", s.handleLanding)
	mux.HandleFunc("GET /{$}", s.handleLanding)
	mux.HandleFunc("GET /robots.txt", s.handleRobots)
	mux.HandleFunc("GET /dl/{name}", s.handleDownloadFile)
	mux.HandleFunc("GET /dl/releases/{name}", s.handleReleaseFile)
	mux.HandleFunc("GET /dl/releases/{id}/manifest.plist", s.handleIOSManifest)
	mux.HandleFunc("GET /api/v1/app/update", s.handleAppUpdate)
	mux.HandleFunc("GET /api/v1/apps", s.handleApps)
	mux.HandleFunc("GET /api/v1/plans", s.handlePlans)
	mux.HandleFunc("POST /api/v1/orders", s.authed(s.handleCreateOrder))
	mux.HandleFunc("GET /api/v1/orders", s.authed(s.handleListOrders))
	mux.HandleFunc("GET /api/v1/orders/{id}", s.authed(s.handleGetOrder))

	// Payment provider callbacks (GET for 易支付, POST for Alipay / mock).
	mux.HandleFunc("/pay/notify/{provider}", s.handleNotify)
	mux.HandleFunc("GET /pay/return", s.handleReturn)
	mux.HandleFunc("GET /pay/mock/{id}", s.handleMockPay)

	mux.HandleFunc("GET /internal/v1/users", s.handleNodeUsers)
	mux.HandleFunc("POST /internal/v1/traffic", s.handleTraffic)

	s.registerAdmin(mux)
	return withRecover(mux)
}

// ---- app endpoints ----

type credentials struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	InviteCode string `json:"invite_code"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      userView  `json:"user"`
}

type userView struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	// RechargeCode is the short, human-readable id the user gives when topping up.
	// Complexity is hidden in the app: this is the only number a user needs.
	RechargeCode string `json:"recharge_code"`
	HasAccount   bool   `json:"has_account"` // email bound (cross-device), vs device-only
	// Linked: this device uses another account's subscription (multi-device);
	// OwnerCode is that account's recharge ID (recharges should go there).
	Linked      bool        `json:"linked"`
	OwnerCode   string      `json:"owner_code,omitempty"`
	Entitlement Entitlement `json:"entitlement"`
}

func (s *Server) viewOf(ctx context.Context, u *store.User) userView {
	v := userView{
		ID: u.ID, Email: u.Email, RechargeCode: rechargeCode(u.ID), HasAccount: u.Email != "",
		Entitlement: s.entitlement(ctx, u),
	}
	if u.Linked() {
		v.Linked, v.OwnerCode = true, rechargeCode(u.PrimaryID)
	}
	return v
}

// rechargeCode formats a user id as a padded, easy-to-read top-up code, e.g. 10042 -> "VP-10042".
func rechargeCode(id int64) string { return fmt.Sprintf("VP-%05d", id) }

// handleDevice is the no-login entry point. The app sends a stable device id
// (persisted in the iOS keychain / Android SSAID so it survives reinstalls);
// the server recognizes a returning device or provisions a new account on the
// spot. No email or password is ever required to start using the service.
func (s *Server) handleDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID   string `json:"device_id"`
		Platform   string `json:"platform"`
		InviteCode string `json:"invite_code"`
	}
	if !decode(w, r, &body) {
		return
	}
	id := strings.TrimSpace(body.DeviceID)
	if len(id) < 8 || len(id) > 128 {
		writeErr(w, http.StatusBadRequest, "invalid_device", "设备标识无效")
		return
	}
	platform := body.Platform
	if platform != "ios" && platform != "android" {
		platform = ""
	}
	existing, lookErr := s.store.UserByDeviceID(r.Context(), id)
	u, err := s.store.DeviceUser(r.Context(), id, platform)
	if err != nil {
		s.internal(w, err)
		return
	}
	// Apply an invite only for a genuinely new device account.
	if errors.Is(lookErr, store.ErrNotFound) || existing == nil {
		s.applyInvite(r, u, strings.TrimSpace(strings.ToUpper(body.InviteCode)))
		if fresh, e := s.store.UserByID(r.Context(), u.ID); e == nil {
			u = fresh
		}
	}
	s.respondToken(w, r, u)
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
	s.applyInvite(r, u, strings.TrimSpace(strings.ToUpper(c.InviteCode)))
	// Re-read so the response reflects any referral bonus just granted.
	if fresh, e := s.store.UserByID(r.Context(), u.ID); e == nil {
		u = fresh
	}
	s.respondToken(w, r, u)
}

// applyInvite links a new user to an inviter's code and grants referral bonuses.
// Failures are non-fatal: the account is already created.
func (s *Server) applyInvite(r *http.Request, u *store.User, code string) {
	ref := s.cat().Referral
	if !ref.Enabled || code == "" {
		return
	}
	inviter, err := s.store.UserByRefCode(r.Context(), code)
	if err != nil || inviter.ID == u.ID {
		return
	}
	res, err := s.store.ApplyReferral(r.Context(), u.ID, inviter.ID,
		ref.RewardPlanID, ref.InviterDays, ref.InviteeDays, ref.MaxInvites, time.Now())
	if err != nil {
		slog.Warn("referral apply", "err", err)
		return
	}
	slog.Info("referral applied", "inviter", inviter.ID, "invitee", u.ID,
		"inviter_days", res.InviterDays, "invitee_days", res.InviteeDays)
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
	s.respondToken(w, r, u)
}

func (s *Server) respondToken(w http.ResponseWriter, r *http.Request, u *store.User) {
	tok, exp, err := s.issuer.Issue(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokenResponse{Token: tok, ExpiresAt: exp, User: s.viewOf(r.Context(), u)})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u *store.User) {
	writeJSON(w, http.StatusOK, s.viewOf(r.Context(), u))
}

type nodeView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Region    string `json:"region"`
	Online    bool   `json:"online"`
	LatencyMs int    `json:"latency_ms"` // measured from the API server, not the user
	// Host / Port let the app measure latency from the user's own network
	// (the address is in the client profile anyway, so it is not a secret).
	Host string `json:"host"`
	Port int    `json:"port"`
	// Uptime is the share of successful health probes in the last hour (0..1).
	Uptime float64 `json:"uptime"`
	// Load is how many users are moving traffic through the node right now.
	Load int `json:"load"`
}

func (s *Server) nodeView(n *config.Node) nodeView {
	v := nodeView{ID: n.ID, Name: n.Name, Region: n.Region, Online: true, Host: n.Address, Port: n.VisionPort, Uptime: 1}
	if s.monitor != nil {
		st := s.monitor.Status(n.ID)
		v.Online, v.LatencyMs, v.Uptime, v.Load = st.Online, st.LatencyMs, st.Uptime, st.ActiveUsers
	}
	return v
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request, u *store.User) {
	nodes := s.cat().NodesForPlan(s.entitlement(r.Context(), u).PlanID)
	out := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, s.nodeView(n))
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request, u *store.User) {
	cat := s.cat()
	e := s.entitlement(r.Context(), u)
	if !e.CanConnect {
		writeErr(w, http.StatusPaymentRequired, "no_entitlement", e.Reason)
		return
	}
	enabled := cat.NodesForPlan(e.PlanID)
	if len(enabled) == 0 {
		writeErr(w, http.StatusServiceUnavailable, "no_nodes", "暂无可用节点")
		return
	}
	mode := profile.ParseMode(r.URL.Query().Get("mode"))
	p := profile.Params{UUID: u.UUID, Mode: mode, AIDomains: cat.AIDomains}

	// Prefer nodes the monitor sees as online; fall back to all enabled nodes.
	healthy := enabled
	if s.monitor != nil {
		var on []*config.Node
		for _, n := range enabled {
			if s.monitor.Status(n.ID).Online {
				on = append(on, n)
			}
		}
		if len(on) > 0 {
			healthy = on
		}
	}

	primary := healthy[0]
	if mode == profile.ModeCluster {
		p.Nodes = healthy
	} else {
		if id := r.URL.Query().Get("node"); id != "" {
			primary = cat.NodeByID(id)
			if primary == nil || !primary.Serving() || !primary.AllowsPlan(e.PlanID) {
				writeErr(w, http.StatusNotFound, "node_not_found", "节点不存在或已下线")
				return
			}
		}
		p.Nodes = []*config.Node{primary}
	}

	cfgJSON, err := profile.Build(p)
	if err != nil {
		s.internal(w, err)
		return
	}
	out := map[string]any{"node": s.nodeView(primary), "mode": mode, "config": cfgJSON}
	// App ≥ 1.2 runs a slimmed core that only accepts compiled (protobuf)
	// configs; older apps keep getting JSON.
	if r.URL.Query().Get("format") == "pb" {
		pb, err := profile.CompileJSON(cfgJSON)
		if err != nil {
			s.internal(w, err)
			return
		}
		out = map[string]any{"node": s.nodeView(primary), "mode": mode, "config": pb, "format": "pb"}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// ---- node agent endpoints ----

type nodeUser struct {
	Email     string `json:"email"`
	UUID      string `json:"uuid"`
	SpeedKbps int    `json:"speed_kbps"` // 0 = unlimited
}

// handleNodeUsers returns users allowed to connect right now, with their speed
// limit. It doubles as the node agent's heartbeat.
//
// Scales to many nodes / users: the user table is read at most every few
// seconds and shared by all nodes (nodeUserCache), and the response carries an
// ETag so an agent whose list hasn't changed gets a 304 with no body.
func (s *Server) handleNodeUsers(w http.ResponseWriter, r *http.Request) {
	node := s.authNode(r)
	if node == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}
	users, err := s.nodeUsers.get(r.Context(), s.store)
	if err != nil {
		s.internal(w, err)
		return
	}
	cat, now := s.cat(), time.Now()
	out := make([]nodeUser, 0, len(users))
	if !node.Frozen { // a frozen node serves nobody
		byID := make(map[int64]*store.User, len(users))
		for _, u := range users {
			byID[u.ID] = u
		}
		for _, u := range users {
			holder := u
			if u.Linked() {
				// Members use their owner's plan; a banned / deleted owner cuts them off.
				if holder = byID[u.PrimaryID]; holder == nil {
					continue
				}
			}
			e := entitlementOf(cat, holder, now)
			if e.CanConnect && node.AllowsPlan(e.PlanID) {
				out = append(out, nodeUser{Email: u.XrayEmail(), UUID: u.UUID, SpeedKbps: e.SpeedKbps})
			}
		}
	}
	if s.monitor != nil {
		s.monitor.Heartbeat(node.ID, len(out))
	}
	body, err := json.Marshal(map[string]any{"users": out, "bandwidth_mbps": node.BandwidthMbps})
	if err != nil {
		s.internal(w, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:12]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(body)
}

// nodeUserCache shares one read of the user table between all node agents.
type nodeUserCache struct {
	mu    sync.Mutex
	at    time.Time
	users []*store.User
}

const nodeUserCacheTTL = 5 * time.Second

func (c *nodeUserCache) get(ctx context.Context, st *store.Store) ([]*store.User, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.users != nil && time.Since(c.at) < nodeUserCacheTTL {
		return c.users, nil
	}
	users, err := st.EnabledUsers(ctx)
	if err != nil {
		return nil, err
	}
	c.users, c.at = users, time.Now()
	return users, nil
}

// invalidate forces the next node sync to re-read users (after admin edits).
func (c *nodeUserCache) invalidate() {
	c.mu.Lock()
	c.users = nil
	c.mu.Unlock()
}

// authNode returns the node whose agent token is in the Authorization header.
func (s *Server) authNode(r *http.Request) *config.Node {
	tok, ok := bearer(r)
	if !ok {
		return nil
	}
	got := sha256.Sum256([]byte(tok))
	return s.cat().NodeByToken(tok, func(a, _ string) bool {
		want := sha256.Sum256([]byte(a))
		return subtle.ConstantTimeCompare(got[:], want[:]) == 1
	})
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
	return decodeBody(w, r, v)
}

// decodeBody decodes JSON without changing the body size limit.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
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
