package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vpnplatform/server/internal/auth"
	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/keys"
	"vpnplatform/server/internal/store"
)

// Admin API, mounted under /admin/api. All responses are JSON.
//
// Layout (one handler group per resource, see the route table in registerAdmin):
//
//	dashboard   GET  /dashboard
//	users       CRUD /users, plus grant / reset-traffic / rotate-uuid actions
//	orders      list /orders, POST /orders/{id}/mark-paid
//	plans|nodes|apps  CRUD on catalog lists (catalogResource, admin_catalog.go)
//	settings    GET/PUT /settings
//	monitor     GET /monitor/events, POST /monitor/check, POST /monitor/test-alert
//
// Catalog edits are validated by config.Catalog.Normalize, persisted, and take
// effect immediately for apps and node agents.

func (s *Server) registerAdmin(mux *http.ServeMux) {
	if s.cfg.Admin.Password == "" {
		return
	}
	mountAdminUI(mux)
	mux.HandleFunc("POST /admin/api/login", s.adminLogin)

	a := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, s.adminAuthed(h)) }
	a("GET /admin/api/dashboard", s.adminDashboard)

	a("GET /admin/api/users", s.adminListUsers)
	a("POST /admin/api/users", s.adminCreateUser)
	a("GET /admin/api/users/{id}", s.adminGetUser)
	a("PATCH /admin/api/users/{id}", s.adminPatchUser)
	a("DELETE /admin/api/users/{id}", s.adminDeleteUser)
	a("POST /admin/api/users/{id}/grant", s.adminGrantPlan)
	a("POST /admin/api/users/{id}/reset-traffic", s.adminUserAction(s.store.ResetTrafficByID))
	a("POST /admin/api/users/{id}/rotate-uuid", s.adminUserAction(s.store.RotateUUIDByID))

	a("GET /admin/api/orders", s.adminListOrders)
	a("POST /admin/api/orders/{id}/mark-paid", s.adminMarkPaid)

	mountCatalogResource(s, a, planResource())
	mountCatalogResource(s, a, nodeResource())
	mountCatalogResource(s, a, appResource())
	mountCatalogResource(s, a, contactResource())
	a("GET /admin/api/contact-types", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"types": config.ContactTypes})
	})
	a("GET /admin/api/nodes/{id}/install", s.adminNodeInstall)
	a("POST /admin/api/nodes/{id}/rotate-token", s.adminNodeRotateToken)
	a("POST /admin/api/nodes/precheck", s.adminPrecheckNode)
	a("POST /admin/api/nodes/{id}/upgrade", s.adminUpgradeNode)
	a("POST /admin/api/nodes/provision", s.adminProvisionNode)
	a("GET /admin/api/geoip", s.adminGeoIP)

	a("GET /admin/api/releases", s.adminListReleases)
	a("POST /admin/api/releases", s.adminCreateRelease)
	a("PATCH /admin/api/releases/{id}", s.adminPatchRelease)
	a("DELETE /admin/api/releases/{id}", s.adminDeleteRelease)

	a("GET /admin/api/i18n", s.adminListLanguages)
	a("GET /admin/api/i18n/base", s.adminI18nBase)
	a("POST /admin/api/i18n/parse", s.adminParsePack)
	a("POST /admin/api/i18n/translate", s.adminTranslate)
	a("PUT /admin/api/i18n/ai", s.adminPutAIConfig)
	a("GET /admin/api/i18n/{code}", s.adminGetLanguage)
	a("PUT /admin/api/i18n/{code}", s.adminPutLanguage)
	a("DELETE /admin/api/i18n/{code}", s.adminDeleteLanguage)

	a("GET /admin/api/backup/info", s.adminBackupInfo)
	a("POST /admin/api/backup", s.adminBackup)
	a("POST /admin/api/backup/restore", s.adminRestore)
	a("POST /admin/api/clone", s.adminClone)

	a("GET /admin/api/settings", s.adminGetSettings)
	a("PUT /admin/api/settings", s.adminPutSettings)

	a("GET /admin/api/monitor/events", s.adminEvents)
	a("POST /admin/api/monitor/check", s.adminCheckNow)
	a("POST /admin/api/monitor/test-alert", s.adminTestAlert)
}

// ---- auth ----

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("admin:" + s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "尝试次数过多，请稍后再试")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	want, got := []byte(s.cfg.Admin.Password), []byte(body.Password)
	if len(want) != len(got) || subtle.ConstantTimeCompare(want, got) != 1 {
		writeErr(w, http.StatusUnauthorized, "bad_password", "密码错误")
		return
	}
	tok, err := s.issuer.IssueAdmin()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (s *Server) adminAuthed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearer(r)
		if !ok || !s.issuer.VerifyAdmin(tok) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "登录已过期，请重新登录")
			return
		}
		h(w, r)
	}
}

// ---- dashboard ----

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	stats, err := s.store.AdminStats(r.Context(), now)
	if err != nil {
		s.internal(w, err)
		return
	}
	trend, err := s.store.Trend(r.Context(), now, 14)
	if err != nil {
		s.internal(w, err)
		return
	}
	cat := s.cat()
	nodes := make([]any, 0, len(cat.Nodes))
	online := 0
	for i := range cat.Nodes {
		v := s.adminNodeView(&cat.Nodes[i])
		if v.Status.Online && !cat.Nodes[i].Disabled {
			online++
		}
		nodes = append(nodes, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":        stats,
		"trend":        trend,
		"nodes":        nodes,
		"nodes_online": online,
		"events":       s.monitor.Events(10),
	})
}

// ---- users ----

func pageParams(r *http.Request) (limit, offset int) {
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("size"))
	page, _ := strconv.Atoi(q.Get("page"))
	if size <= 0 || size > 200 {
		size = 20
	}
	if page <= 0 {
		page = 1
	}
	return size, (page - 1) * size
}

type adminUser struct {
	ID           int64       `json:"id"`
	Email        string      `json:"email"`
	RechargeCode string      `json:"recharge_code"`
	DeviceID     string      `json:"device_id"`
	Platform     string      `json:"platform"`
	PrimaryID    int64       `json:"primary_id"` // >0: this device shares that user's plan
	Disabled     bool        `json:"disabled"`
	Note         string      `json:"note"`
	RefCode      string      `json:"ref_code"`
	InvitedBy    int64       `json:"invited_by"`
	UUID         string      `json:"uuid"`
	CreatedAt    time.Time   `json:"created_at"`
	PlanID       string      `json:"plan_id"`    // stored paid plan (may be expired)
	ExpiresAt    *time.Time  `json:"expires_at"` // stored expiry
	Entitlement  Entitlement `json:"entitlement"`
}

func (s *Server) adminUserView(u *store.User) adminUser {
	return adminUser{
		ID: u.ID, Email: u.Email, RechargeCode: rechargeCode(u.ID),
		DeviceID: u.DeviceID, Platform: u.Platform, PrimaryID: u.PrimaryID,
		Disabled: u.Disabled, Note: u.Note, RefCode: u.RefCode,
		InvitedBy: u.InvitedBy, UUID: u.UUID, CreatedAt: u.CreatedAt,
		PlanID: u.PlanID, ExpiresAt: u.ExpiresAt,
		Entitlement: entitlementOf(s.cat(), u, time.Now()),
	}
}

func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	users, total, err := s.store.ListUsers(r.Context(), store.UserFilter{
		Query: r.URL.Query().Get("q"), Status: r.URL.Query().Get("status"), Limit: limit, Offset: offset,
	})
	if err != nil {
		s.internal(w, err)
		return
	}
	items := make([]adminUser, 0, len(users))
	for _, u := range users {
		items = append(items, s.adminUserView(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) adminCreateUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		PlanID   string `json:"plan_id"`
		Days     int    `json:"days"`
		Note     string `json:"note"`
	}
	if !decode(w, r, &body) {
		return
	}
	email, ok := normalizeEmail(body.Email)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid_email", "邮箱格式不正确")
		return
	}
	if len(body.Password) < 8 || len(body.Password) > 72 {
		writeErr(w, http.StatusBadRequest, "weak_password", "密码长度需为 8-72 位")
		return
	}
	if body.PlanID != "" && (s.cat().Plan(body.PlanID) == nil || body.Days <= 0) {
		writeErr(w, http.StatusBadRequest, "bad_plan", "套餐或天数无效")
		return
	}
	hash, err := auth.HashPassword(body.Password)
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
	if body.PlanID != "" {
		if err := s.store.GrantPlanByID(r.Context(), u.ID, body.PlanID, body.Days); err != nil {
			s.internal(w, err)
			return
		}
	}
	if body.Note != "" {
		_ = s.store.SetNoteByID(r.Context(), u.ID, body.Note)
	}
	s.adminReturnUser(w, r, u.ID)
}

func (s *Server) adminGetUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	usage, err := s.store.Usage(r.Context(), id, time.Now().AddDate(0, 0, -29).Format("2006-01-02"))
	if err != nil {
		s.internal(w, err)
		return
	}
	orders, _, err := s.store.ListOrders(r.Context(), store.OrderFilter{UserID: u.ID, Limit: 50})
	if err != nil {
		s.internal(w, err)
		return
	}
	invited, _ := s.store.CountInvited(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{
		"user": s.adminUserView(u), "usage": usage, "orders": orders, "invited_count": invited,
	})
}

// adminPatchUser updates any subset of: disabled, note, password, plan_id + expires_at.
func (s *Server) adminPatchUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		Disabled  *bool   `json:"disabled"`
		Note      *string `json:"note"`
		Password  *string `json:"password"`
		PlanID    *string `json:"plan_id"`
		ExpiresAt *string `json:"expires_at"` // RFC3339 or "" to clear
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	if body.Password != nil {
		if len(*body.Password) < 8 || len(*body.Password) > 72 {
			writeErr(w, http.StatusBadRequest, "weak_password", "密码长度需为 8-72 位")
			return
		}
		hash, err := auth.HashPassword(*body.Password)
		if err != nil {
			s.internal(w, err)
			return
		}
		if err := s.store.SetPasswordHashByID(ctx, id, hash); err != nil {
			s.adminStoreErr(w, err)
			return
		}
	}
	if body.PlanID != nil {
		var exp *time.Time
		if body.ExpiresAt != nil && *body.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, *body.ExpiresAt)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad_date", "到期时间格式错误")
				return
			}
			exp = &t
		}
		if *body.PlanID != "" && s.cat().Plan(*body.PlanID) == nil {
			writeErr(w, http.StatusBadRequest, "bad_plan", "套餐不存在")
			return
		}
		if err := s.store.SetPlanByID(ctx, id, *body.PlanID, exp); err != nil {
			s.adminStoreErr(w, err)
			return
		}
	}
	if body.Disabled != nil {
		if err := s.store.SetDisabledByID(ctx, id, *body.Disabled); err != nil {
			s.adminStoreErr(w, err)
			return
		}
	}
	if body.Note != nil {
		if err := s.store.SetNoteByID(ctx, id, strings.TrimSpace(*body.Note)); err != nil {
			s.adminStoreErr(w, err)
			return
		}
	}
	s.adminReturnUser(w, r, id)
}

func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) adminGrantPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		PlanID string `json:"plan_id"`
		Days   int    `json:"days"`
	}
	if !decode(w, r, &body) {
		return
	}
	if p := s.cat().Plan(body.PlanID); p == nil || p.ID == s.cat().FreePlan || body.Days <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_plan", "请选择付费套餐并填写天数")
		return
	}
	if err := s.store.GrantPlanByID(r.Context(), id, body.PlanID, body.Days); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	s.adminReturnUser(w, r, id)
}

func (s *Server) adminUserAction(fn func(ctx context.Context, id int64) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if err := fn(r.Context(), id); err != nil {
			s.adminStoreErr(w, err)
			return
		}
		s.adminReturnUser(w, r, id)
	}
}

func (s *Server) adminReturnUser(w http.ResponseWriter, r *http.Request, id int64) {
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.adminUserView(u))
}

func (s *Server) adminStoreErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "记录不存在")
		return
	}
	s.internal(w, err)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad_id", "无效的 ID")
		return 0, false
	}
	return id, true
}

// ---- orders ----

func (s *Server) adminListOrders(w http.ResponseWriter, r *http.Request) {
	limit, offset := pageParams(r)
	items, total, err := s.store.ListOrders(r.Context(), store.OrderFilter{
		Query: r.URL.Query().Get("q"), Status: r.URL.Query().Get("status"), Limit: limit, Offset: offset,
	})
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) adminMarkPaid(w http.ResponseWriter, r *http.Request) {
	o, err := s.store.OrderByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	days := 30
	if p := s.cat().Plan(o.PlanID); p != nil {
		days = p.DurationDays
	}
	granted, err := s.store.MarkOrderPaidManually(r.Context(), o.ID, days)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"granted": granted})
}

// ---- nodes (extra actions; CRUD is in admin_catalog.go) ----

type adminNodeStatusView struct {
	Online       bool      `json:"online"`
	LatencyMs    int       `json:"latency_ms"`
	Uptime       float64   `json:"uptime"`
	History      any       `json:"history"`
	LastCheck    time.Time `json:"last_check"`
	AgentOnline  bool      `json:"agent_online"`
	AgentSeen    time.Time `json:"agent_seen"`
	Users        int       `json:"users"`
	ActiveUsers  int       `json:"active_users"` // users moving traffic right now
	TrafficBytes int64     `json:"traffic_bytes"`
}

type adminNodeResp struct {
	config.Node
	Status adminNodeStatusView `json:"status"`
}

func (s *Server) adminNodeView(n *config.Node) adminNodeResp {
	st := s.monitor.Status(n.ID)
	out := adminNodeResp{Node: *n, Status: adminNodeStatusView{
		Online: st.Online, LatencyMs: st.LatencyMs, Uptime: st.Uptime, History: st.History,
		LastCheck: st.LastCheck, AgentOnline: st.AgentOnline, AgentSeen: st.AgentSeen,
		Users: st.Users, ActiveUsers: st.ActiveUsers, TrafficBytes: st.TrafficBytes,
	}}
	out.Node.RealityPrivateKey = "" // shown only via /install
	out.Node.Token = maskSecret(n.Token)
	return out
}

func maskSecret(s string) string {
	if len(s) <= 8 {
		return "••••"
	}
	return s[:4] + "••••" + s[len(s)-4:]
}

// adminNodeInstall returns the node agent config and install commands.
func (s *Server) adminNodeInstall(w http.ResponseWriter, r *http.Request) {
	n := s.cat().NodeByID(r.PathValue("id"))
	if n == nil {
		writeErr(w, http.StatusNotFound, "not_found", "节点不存在")
		return
	}
	if n.RealityPrivateKey == "" {
		writeErr(w, http.StatusConflict, "no_private_key",
			"该节点的私钥不在后台（由旧版配置导入），请在节点服务器上查看 /etc/vpn/node.json")
		return
	}
	target := n.RealityTarget
	if target == "" {
		target = n.Reality.ServerName + ":443"
	}
	apiURL := s.cfg.PublicURL
	if apiURL == "" {
		apiURL = "https://YOUR-API-DOMAIN"
	}
	nodeCfg := map[string]any{
		"api_url":               apiURL,
		"token":                 n.Token,
		"sync_interval_seconds": 30,
		"log_level":             "warning",
		"listen":                "0.0.0.0",
		"vision_port":           n.VisionPort,
		"xhttp_path":            n.XHTTPPath,
		"xhttp_internal":        "127.0.0.1:2001",
		"reality": map[string]any{
			"private_key":  n.RealityPrivateKey,
			"short_ids":    []string{n.Reality.ShortID},
			"target":       target,
			"server_names": []string{n.Reality.ServerName},
		},
	}
	b, _ := json.MarshalIndent(nodeCfg, "", "  ")
	steps := fmt.Sprintf(`# 在节点服务器上（Debian/Ubuntu，root）执行：
useradd -r -s /usr/sbin/nologin vpn 2>/dev/null || true
mkdir -p /etc/vpn
# 1. 上传 vpn-node 二进制到 /usr/local/bin/vpn-node（make linux 产物）
# 2. 把下面的 node.json 写入 /etc/vpn/node.json
chmod 640 /etc/vpn/node.json && chown root:vpn /etc/vpn/node.json
# 3. 安装 systemd 服务（server/deploy/vpn-node.service）并启动
systemctl enable --now vpn-node
# 4. 防火墙放行 TCP %d`, n.VisionPort)
	writeJSON(w, http.StatusOK, map[string]any{"node_json": string(b), "steps": steps})
}

func (s *Server) adminNodeRotateToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tok, err := keys.RandomToken(32)
	if err != nil {
		s.internal(w, err)
		return
	}
	_, err = s.live.Update(func(c *config.Catalog) error {
		n := c.NodeByID(id)
		if n == nil {
			return errNotFound
		}
		n.Token = tok
		return nil
	}, s.saveCatalog(r))
	if s.catalogErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": tok})
}

// ---- settings ----

type settingsView struct {
	FreePlan          string                   `json:"free_plan"`
	TrafficPeriodDays int                      `json:"traffic_period_days"`
	Referral          config.ReferralConfig    `json:"referral"`
	Monitor           config.MonitorConfig     `json:"monitor"`
	AIDomains         []string                 `json:"ai_domains"`
	Share             config.ShareConfig       `json:"share"`
	MultiDevice       config.MultiDeviceConfig `json:"multi_device"`
}

func settingsOf(c *config.Catalog) settingsView {
	return settingsView{c.FreePlan, c.TrafficPeriodDays, c.Referral, c.Monitor, c.AIDomains, c.Share, c.MultiDevice}
}

func (s *Server) adminGetSettings(w http.ResponseWriter, _ *http.Request) {
	c := s.cat()
	methods := []string{}
	for _, p := range s.providers {
		for _, m := range p.Methods() {
			methods = append(methods, m.Name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settingsOf(c),
		// Read-only: these come from api.json / environment.
		"system": map[string]any{
			"public_url":      s.cfg.PublicURL,
			"allow_register":  s.cfg.AllowRegister,
			"payment_methods": methods,
			"mock_payments":   s.cfg.Payments.Mock,
		},
	})
}

func (s *Server) adminPutSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsView
	if !decode(w, r, &in) {
		return
	}
	next, err := s.live.Update(func(c *config.Catalog) error {
		c.FreePlan = in.FreePlan
		c.TrafficPeriodDays = in.TrafficPeriodDays
		c.Referral = in.Referral
		c.Monitor = in.Monitor
		c.AIDomains = compactStrings(in.AIDomains)
		c.MultiDevice = in.MultiDevice
		c.Share = config.ShareConfig{AndroidURL: strings.TrimSpace(in.Share.AndroidURL), IOSURL: strings.TrimSpace(in.Share.IOSURL), Slogan: strings.TrimSpace(in.Share.Slogan)}
		return nil
	}, s.saveCatalog(r))
	if s.catalogErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, settingsOf(next))
}

func compactStrings(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- monitor ----

func (s *Server) adminEvents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": s.monitor.Events(200)})
}

func (s *Server) adminCheckNow(w http.ResponseWriter, r *http.Request) {
	s.monitor.CheckNow(r.Context())
	cat := s.cat()
	items := make([]adminNodeResp, 0, len(cat.Nodes))
	for i := range cat.Nodes {
		items = append(items, s.adminNodeView(&cat.Nodes[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) adminTestAlert(w http.ResponseWriter, _ *http.Request) {
	if err := s.monitor.TestAlert(); err != nil {
		writeErr(w, http.StatusBadGateway, "alert_failed", "发送失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
