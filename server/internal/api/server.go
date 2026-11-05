// Package api is the control-plane HTTP API used by the app and by node agents.
package api

import (
	"compress/gzip"
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
	"net/netip"
	"os"
	"sort"
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
	cfg             *config.API         // static settings from api.json
	live            *config.LiveCatalog // admin-editable catalog (plans, nodes, apps, settings)
	store           *store.Store
	issuer          *auth.Issuer
	limiter         *ipLimiter
	providers       []payment.Provider
	mock            *payment.Mock
	monitor         *monitor.Monitor
	nodeUsers       nodeUserCache
	risk            riskState     // blocked IPs / users (risk.go)
	ips             ipRecorder    // buffered IP records (risk.go)
	segCache        segmentCache  // user segmentation (segments.go)
	gw              gatewayCache  // payment link gateway (gateway.go)
	guard           guardState    // request flood / abuse protection (guard.go)
	events          eventBuffer   // app analytics (analytics.go)
	iap             iapVerifier   // App Store receipt verification (iap.go), nil = not configured
	gplay           gplayVerifier // Google Play purchase verification (iap_google.go), nil = not configured
	nonces          nonceCache    // request nonces seen (secure.go)
	envLogged       sync.Map      // user id -> last integrity log (secure.go)
	signups         signupGuard   // new device accounts per network (signup_guard.go)
	trafficInflight sync.Map      // node/batch of traffic reports being applied (billing.go)
}

func New(cfg *config.API, live *config.LiveCatalog, st *store.Store, issuer *auth.Issuer, mon *monitor.Monitor) (*Server, error) {
	providers, mock, err := newProviders(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, live: live, store: st, issuer: issuer, limiter: newIPLimiter(rate.Every(6*time.Second), 10),
		providers: providers, mock: mock, monitor: mon}
	if a := cfg.Payments.AppleIAP; a != nil && a.SharedSecret != "" {
		if a.BundleID == "" {
			// Without it, a receipt from any app (with a product id named like
			// a plan) would be accepted.
			slog.Error("apple_iap.bundle_id is not set: in-app purchase stays off")
		} else {
			s.iap = newAppleVerifier(a.SharedSecret, a.BundleID)
		}
	}
	if g := cfg.Payments.GooglePlay; g != nil && g.PackageName != "" {
		saJSON := []byte(os.Getenv("VPN_GOOGLE_SA_JSON"))
		if len(saJSON) == 0 && g.ServiceAccountFile != "" {
			b, err := os.ReadFile(g.ServiceAccountFile)
			if err != nil {
				return nil, fmt.Errorf("read google_play.service_account_file: %w", err)
			}
			saJSON = b
		}
		if len(saJSON) == 0 {
			slog.Error("google_play configured without a service account key: Play purchases stay off")
		} else {
			v, err := newGoogleVerifier(saJSON, g.PackageName)
			if err != nil {
				return nil, fmt.Errorf("google_play: %w", err)
			}
			s.gplay = v
		}
	}
	return s, nil
}

// cat returns the current catalog snapshot (read-only).
func (s *Server) cat() *config.Catalog { return s.live.Get() }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("POST /api/v1/auth/device", s.rateLimited(s.handleDevice))
	mux.HandleFunc("POST /api/v1/auth/register", s.rateLimited(s.handleRegister))
	mux.HandleFunc("POST /api/v1/auth/login", s.rateLimited(s.handleLogin))
	mux.HandleFunc("POST /api/v1/auth/rekey", s.rateLimited(s.handleRekey))
	mux.HandleFunc("GET /api/v1/me", s.authed(s.handleMe))
	mux.HandleFunc("GET /api/v1/nodes", s.authed(s.handleNodes))
	mux.HandleFunc("GET /api/v1/profile", s.authed(s.handleProfile))
	mux.HandleFunc("GET /api/v1/usage", s.authed(s.handleUsage))
	mux.HandleFunc("GET /api/v1/referral", s.authed(s.handleReferral))
	mux.HandleFunc("POST /api/v1/referral/bind", s.authed(s.handleReferralBind))
	mux.HandleFunc("GET /api/v1/contacts", s.optionalSession(s.handleContacts))
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
	mux.HandleFunc("GET /site/{name}", s.handleSiteAsset)
	mux.HandleFunc("GET /dl/{name}", s.handleDownloadFile)
	mux.HandleFunc("GET /dl/releases/{name}", s.handleReleaseFile)
	mux.HandleFunc("GET /dl/releases/{id}/manifest.plist", s.handleIOSManifest)
	mux.HandleFunc("GET /api/v1/app/update", s.optionalSession(s.handleAppUpdate))
	mux.HandleFunc("GET /api/v1/apps", s.optionalSession(s.handleApps))
	mux.HandleFunc("GET /api/v1/i18n", s.optionalSession(s.handleLanguages))
	mux.HandleFunc("GET /api/v1/i18n/{code}", s.optionalSession(s.handleLanguagePack))
	mux.HandleFunc("GET /api/v1/plans", s.optionalSession(s.handlePlans))
	mux.HandleFunc("POST /api/v1/orders", s.authed(s.handleCreateOrder))
	mux.HandleFunc("GET /api/v1/orders", s.authed(s.handleListOrders))
	mux.HandleFunc("GET /api/v1/orders/{id}", s.authed(s.handleGetOrder))
	mux.HandleFunc("POST /api/v1/iap/verify", s.authed(s.handleIAPVerify))
	mux.HandleFunc("POST /api/v1/iap/restore", s.authed(s.handleIAPRestore))
	mux.HandleFunc("POST /api/v1/iap/google/verify", s.authed(s.handleGPlayVerify))
	mux.HandleFunc("GET /legal/{doc}", s.handleLegal)

	// Payment provider callbacks (GET for 易支付, POST for Alipay / mock).
	mux.HandleFunc("/pay/notify/{provider}", s.handleNotify)
	mux.HandleFunc("GET /pay/return", s.handleReturn)
	mux.HandleFunc("GET /pay/mock/{id}", s.handleMockPay)
	mux.HandleFunc("POST "+gatewayNotifyPath, s.handleGatewayNotify)
	mux.HandleFunc("POST /api/v1/events", s.optionalSession(s.handleEvents))
	mux.HandleFunc("POST /api/v1/crash", s.optionalSession(s.handleCrash))
	mux.HandleFunc("GET /api/v1/ads", s.optionalSession(s.handleAds))
	mux.HandleFunc("GET /dl/ads/{name}", s.handleAdImage)

	mux.HandleFunc("GET /internal/v1/users", s.handleNodeUsers)
	mux.HandleFunc("POST /internal/v1/traffic", s.handleTraffic)

	s.registerAdmin(mux)
	return withSecurityHeaders(withServerTime(s.withAccessLog(withRecover(s.withGuard(s.withRiskIP(s.withAPIOnlyHosts(mux)))))))
}

// withAPIOnlyHosts: on tls.api_only domains (an old domain kept for app
// versions that have it built in, which resolves straight to this server)
// only what apps call is served. The download site and the admin panel are
// not: someone probing this IP with that name sees no VPN website, only
// JSON 404s. Everything else keeps working on the main domain.
func (s *Server) withAPIOnlyHosts(next http.Handler) http.Handler {
	if len(s.cfg.TLS.APIOnly) == 0 {
		return next
	}
	only := map[string]bool{}
	for _, d := range s.cfg.TLS.APIOnly {
		only[strings.ToLower(d)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if r.TLS != nil && r.TLS.ServerName != "" {
			host = r.TLS.ServerName
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if only[strings.ToLower(host)] && !apiOnlyPath(r.URL.Path) {
			writeErr(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// apiOnlyPath: what installed apps, node agents and payment callbacks use.
func apiOnlyPath(p string) bool {
	for _, pre := range []string{"/api/", "/internal/", "/pay/", "/legal/", "/dl/releases/", "/.well-known/acme-challenge/"} {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return p == "/healthz"
}

// ---- app endpoints ----

type credentials struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	InviteCode string `json:"invite_code"`
	Pub        string `json:"pub"` // X25519 key for the session (secure.go)
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
		Pub        string `json:"pub"` // X25519 key for the session (secure.go); "" = app before 1.2.5
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
	if !knownPlatform(platform) {
		platform = ""
	}
	existing, lookErr := s.store.UserByDeviceID(r.Context(), id)
	if lookErr != nil && !errors.Is(lookErr, store.ErrNotFound) {
		s.internal(w, lookErr)
		return
	}
	isNew := existing == nil
	invite := strings.TrimSpace(strings.ToUpper(body.InviteCode))
	if isNew {
		ok, credit := s.signups.allow(s.clientIP(r), s.cat().Guard.NewDevicesPerIPDay, invite != "", time.Now())
		if !ok {
			slog.Warn("new device accounts over the daily limit", "ip", s.clientIP(r))
			writeErr(w, http.StatusTooManyRequests, "too_many_devices", "当前网络今天新开通的设备过多，请明天再试或更换网络")
			return
		}
		if !credit {
			invite = ""
		}
	}
	u, err := s.store.DeviceUser(r.Context(), id, platform)
	if err != nil {
		s.internal(w, err)
		return
	}
	// Apply an invite only for a genuinely new device account.
	if isNew {
		s.applyInvite(r, u, invite)
		if fresh, e := s.store.UserByID(r.Context(), u.ID); e == nil {
			u = fresh
		}
	}
	s.respondToken(w, r, u, body.Pub)
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
	s.respondToken(w, r, u, c.Pub)
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
	s.respondToken(w, r, u, c.Pub)
}

func (s *Server) respondToken(w http.ResponseWriter, r *http.Request, u *store.User, clientPub string) {
	if _, blocked := s.blockedUser(r.Context(), u); blocked {
		writeErr(w, http.StatusForbidden, "account_blocked", "账号已被限制使用，请联系客服")
		return
	}
	s.recordIP(u.ID, s.clientIP(r), "api", "", 1)
	if clientPub != "" {
		s.respondSession(w, r, u, clientPub)
		return
	}
	if s.cat().Guard.RequireSigned {
		writeErr(w, http.StatusUpgradeRequired, "upgrade_required", "当前版本过旧，请更新到最新版本")
		return
	}
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
	p.DirectDomains, p.DirectIPs = s.apiEndpoints()
	p.DirectHosts = s.apiHosts(p.DirectDomains, p.DirectIPs)

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
	} else if mode == profile.ModeAI {
		// AI 优先: AI services drop long sessions on any hiccup, so pick the
		// most stable nodes (uptime first, then latency) rather than the
		// one the user selected; up to 3 for automatic failover.
		p.Nodes = s.stableNodes(healthy, 3)
		primary = p.Nodes[0]
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
	// The compiled profile carries the China routing table and is large
	// (~200 KB); gzip cuts it several-fold, which is the main win for a slow
	// connect on a poor network. The app's dart:io client sends
	// Accept-Encoding: gzip and decompresses transparently.
	writeJSONGzip(w, r, http.StatusOK, out)
}

// writeJSONGzip is writeJSON, but gzips a large body when the client accepts
// it (small bodies are sent as-is: compression would not pay off).
func writeJSONGzip(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "服务器错误")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if len(b) > 1400 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.WriteHeader(status)
		gz := gzip.NewWriter(w)
		_, _ = gz.Write(b)
		_ = gz.Close()
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(b)
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
	s.riskFresh(r.Context())
	blocked := s.blockedUserIDs()
	if !node.Frozen { // a frozen node serves nobody
		byID := make(map[int64]*store.User, len(users))
		for _, u := range users {
			byID[u.ID] = u
		}
		for _, u := range users {
			if blocked[u.ID] || (u.Linked() && blocked[u.PrimaryID]) {
				continue // risk control: blocked account (or its owner)
			}
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
	blockedIPs := s.nodeBlockedIPs()
	if blockedIPs == nil {
		blockedIPs = []string{}
	}
	body, err := json.Marshal(map[string]any{"users": out, "bandwidth_mbps": node.BandwidthMbps, "blocked_ips": blockedIPs})
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
		uid, key, err := s.issuer.VerifySession(tok)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录")
			return
		}
		if key == nil && s.cat().Guard.RequireSigned {
			// 401 so the app signs in again (getting a session key); apps too
			// old to do that are told to update.
			writeErr(w, http.StatusUnauthorized, "key_required", "当前版本过旧，请更新到最新版本")
			return
		}
		sw, done, se := s.secureSession(w, r, uid, key)
		if se != nil {
			se.write(w)
			return
		}
		defer done()
		w = sw
		u, err := s.store.UserByID(r.Context(), uid)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "账号不存在")
			return
		} else if err != nil {
			s.internal(w, err)
			return
		}
		if _, blocked := s.blockedUser(r.Context(), u); blocked {
			writeErr(w, http.StatusForbidden, "account_blocked", "账号已被限制使用，请联系客服")
			return
		}
		s.recordIP(u.ID, s.clientIP(r), "api", "", 1)
		h(w, r, u)
	}
}

func (s *Server) rateLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.allowIP("", s.clientIP(r)) {
			writeErr(w, http.StatusTooManyRequests, "rate_limited", "请求过于频繁，请稍后再试")
			return
		}
		h(w, r)
	}
}

// clientIP is the caller's address. Behind our own reverse proxy
// (trust_proxy_header) it is the LAST X-Forwarded-For entry: the one the
// proxy added. Earlier entries come from the client and can be forged
// ("X-Forwarded-For: 127.0.0.1" would otherwise pass as loopback and skip
// the request guard and rate limits). The header is only honoured when the
// request really comes from a proxy on loopback / a private network.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.cfg.TrustCloudflare {
		if ip := cloudflareClientIP(r, host); ip != "" {
			return ip
		}
	}
	if s.cfg.TrustProxyHeader {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if peer := net.ParseIP(host); peer != nil && (peer.IsLoopback() || peer.IsPrivate()) {
				parts := strings.Split(xff, ",")
				if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
					return ip
				}
			}
		}
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

const limiterMaxKeys = 200_000

func (l *ipLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		if len(l.entries) >= limiterMaxKeys {
			// Flooded with distinct keys: forget an arbitrary one (map order
			// is random) instead of growing without bound. Refusing new keys
			// would lock every new client out, admins included; forgetting
			// one only resets that key's budget.
			for k := range l.entries {
				delete(l.entries, k)
				break
			}
		}
		e = &limiterEntry{l: rate.NewLimiter(l.every, l.burst)}
		l.entries[key] = e
	}
	e.seen = time.Now()
	return e.l.Allow()
}

// allowIP limits a client address. IPv6 is limited per /64: one host can use
// every address of its /64, so a per-address limit would be no limit.
func (l *ipLimiter) allowIP(prefix, ip string) bool {
	if a, err := netip.ParseAddr(ip); err == nil && a.Unmap().Is6() {
		if p, err := a.Unmap().Prefix(64); err == nil {
			ip = p.String()
		}
	}
	return l.allow(prefix + ip)
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

// stableNodes orders nodes by monitored stability: uptime over the last hour,
// then current load, then latency from the API. Returns at most n.
func (s *Server) stableNodes(nodes []*config.Node, n int) []*config.Node {
	out := append([]*config.Node(nil), nodes...)
	if s.monitor != nil {
		st := make(map[string]monitor.Status, len(out))
		for _, x := range out {
			st[x.ID] = s.monitor.Status(x.ID)
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := st[out[i].ID], st[out[j].ID]
			// Uptime differences under 2 % are noise.
			if d := a.Uptime - b.Uptime; d > 0.02 || d < -0.02 {
				return d > 0
			}
			if a.ActiveUsers/10 != b.ActiveUsers/10 {
				return a.ActiveUsers < b.ActiveUsers
			}
			return a.LatencyMs < b.LatencyMs
		})
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}
