package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/geoip"
	"vpnplatform/server/internal/ipclass"
	"vpnplatform/server/internal/store"
)

func (s *Server) registerAdminRisk(a func(string, http.HandlerFunc)) {
	a("GET /admin/api/risk/ips", s.adminRiskIPs)
	a("GET /admin/api/risk/suspicious", s.adminRiskSuspicious)
	a("GET /admin/api/risk/rules", s.adminRiskRules)
	a("POST /admin/api/risk/rules", s.adminRiskAddRule)
	a("DELETE /admin/api/risk/rules", s.adminRiskDeleteRule)
	a("GET /admin/api/risk/config", s.adminRiskConfig)
	a("PUT /admin/api/risk/config", s.adminRiskPutConfig)
	a("GET /admin/api/risk/geo", s.adminRiskGeo)

	a("GET /admin/api/segments", s.adminSegments)
	a("GET /admin/api/segments/{key}/users", s.adminSegmentUsers)
	a("GET /admin/api/segments/{key}/export", s.adminSegmentExport)
	a("PUT /admin/api/segments/config", s.adminSegmentConfig)
}

// ---- IP records ----

type ipView struct {
	store.IPRecord
	RechargeCode string `json:"recharge_code"`
	Class        string `json:"class"` // cn | abroad | private
	NodeName     string `json:"node_name"`
	Blocked      bool   `json:"blocked"`
}

func (s *Server) ipViews(r *http.Request, items []store.IPRecord) []ipView {
	cat := s.cat()
	out := make([]ipView, 0, len(items))
	for _, it := range items {
		v := ipView{IPRecord: it, RechargeCode: rechargeCode(it.UserID), Class: ipclass.Of(it.IP)}
		if n := cat.NodeByID(it.NodeID); n != nil {
			v.NodeName = n.Name
		}
		_, v.Blocked = s.blockedIP(r.Context(), it.IP)
		out = append(out, v)
	}
	return out
}

func (s *Server) adminRiskIPs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, offset := pageParams(r)
	f := store.IPFilter{IP: q.Get("ip"), Source: q.Get("source"), Limit: limit, Offset: offset}
	if u := strings.TrimSpace(q.Get("user")); u != "" {
		if f.UserID = parseUserRef(u); f.UserID <= 0 {
			writeErr(w, http.StatusBadRequest, "bad_user", "用户格式：VP-00010 或数字 ID")
			return
		}
	}
	if d, _ := strconv.Atoi(q.Get("days")); d > 0 {
		f.Since = time.Now().AddDate(0, 0, -d).Unix()
	}
	page, err := s.store.UserIPs(r.Context(), f)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.ipViews(r, page.Items), "total": page.Total})
}

// parseUserRef accepts "VP-00010", "vp-10" or "10".
func parseUserRef(v string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(v)), "VP-"), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

type suspectUser struct {
	store.MultiIPUser
	RechargeCode string     `json:"recharge_code"`
	PlanID       string     `json:"plan_id"`
	ExpiresAt    *time.Time `json:"expires_at"`
	Blocked      bool       `json:"blocked"`
	Disabled     bool       `json:"disabled"`
}

type suspectIP struct {
	store.SharedIP
	Class   string   `json:"class"`
	Codes   []string `json:"recharge_codes"`
	Blocked bool     `json:"blocked"`
}

func (s *Server) adminRiskSuspicious(w http.ResponseWriter, r *http.Request) {
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 || hours > 24*30 {
		hours = 24
	}
	cfg := s.cat().Risk
	since := time.Now().Add(-time.Duration(hours) * time.Hour).Unix()
	ctx := r.Context()
	multi, err := s.store.MultiIPUsers(ctx, since, cfg.MaxIPsPerUser+1, 100)
	if err != nil {
		s.internal(w, err)
		return
	}
	shared, err := s.store.SharedIPs(ctx, since, cfg.MaxUsersPerIP+1, 100)
	if err != nil {
		s.internal(w, err)
		return
	}
	ids := make([]int64, 0, len(multi))
	for _, m := range multi {
		ids = append(ids, m.UserID)
	}
	users, err := s.store.UsersByIDs(ctx, ids)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.riskFresh(ctx)
	blocked := s.blockedUserIDs()
	mu := make([]suspectUser, 0, len(multi))
	for _, m := range multi {
		v := suspectUser{MultiIPUser: m, RechargeCode: rechargeCode(m.UserID), Blocked: blocked[m.UserID]}
		if u := users[m.UserID]; u != nil {
			v.PlanID, v.ExpiresAt, v.Disabled = u.PlanID, u.ExpiresAt, u.Disabled
		}
		mu = append(mu, v)
	}
	si := make([]suspectIP, 0, len(shared))
	for _, sh := range shared {
		v := suspectIP{SharedIP: sh, Class: ipclass.Of(sh.IP)}
		_, v.Blocked = s.blockedIP(ctx, sh.IP)
		for _, id := range sh.UserIDs {
			v.Codes = append(v.Codes, rechargeCode(id))
		}
		si = append(si, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"hours": hours, "multi_ip_users": mu, "shared_ips": si, "config": cfg})
}

// ---- rules ----

type ruleView struct {
	store.RiskRule
	Active       bool   `json:"active"`
	RechargeCode string `json:"recharge_code,omitempty"`
}

func (s *Server) adminRiskRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.RiskRules(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	now := time.Now()
	out := make([]ruleView, 0, len(rules))
	for _, rl := range rules {
		v := ruleView{RiskRule: rl, Active: rl.Active(now)}
		if rl.Kind == "user" {
			if id, err := strconv.ParseInt(rl.Value, 10, 64); err == nil {
				v.RechargeCode = rechargeCode(id)
			}
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) adminRiskAddRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string `json:"kind"`  // ip | user
		Value  string `json:"value"` // 1.2.3.4, 1.2.3.0/24, VP-00010
		Reason string `json:"reason"`
		Days   int    `json:"days"` // 0 = permanent
	}
	if !decode(w, r, &in) {
		return
	}
	rule := store.RiskRule{Kind: in.Kind, Reason: strings.TrimSpace(in.Reason)}
	if len([]rune(rule.Reason)) > 200 {
		writeErr(w, http.StatusBadRequest, "bad_reason", "原因最多 200 字")
		return
	}
	switch in.Kind {
	case "ip":
		p, err := parseIPRule(in.Value)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_ip", "IP 格式：1.2.3.4、1.2.3.0/24 或 IPv6")
			return
		}
		// A typo like 1.0.0.0/1 would cut off half the internet.
		if (p.Addr().Is4() && p.Bits() < 16) || (p.Addr().Is6() && p.Bits() < 32) {
			writeErr(w, http.StatusBadRequest, "range_too_wide", "范围过大：IPv4 最大 /16，IPv6 最大 /32")
			return
		}
		if p.Addr().IsLoopback() || p.Addr().IsUnspecified() {
			writeErr(w, http.StatusBadRequest, "bad_ip", "不能封禁本机地址")
			return
		}
		rule.Value = p.String()
		if p.Bits() == p.Addr().BitLen() {
			rule.Value = p.Addr().String()
		}
	case "user":
		id := parseUserRef(in.Value)
		if id <= 0 {
			writeErr(w, http.StatusBadRequest, "bad_user", "用户格式：VP-00010 或数字 ID")
			return
		}
		if _, err := s.store.UserByID(r.Context(), id); err != nil {
			s.adminStoreErr(w, err)
			return
		}
		rule.Value = strconv.FormatInt(id, 10)
	default:
		writeErr(w, http.StatusBadRequest, "bad_kind", "类型只能是 ip 或 user")
		return
	}
	if in.Days < 0 || in.Days > 3650 {
		writeErr(w, http.StatusBadRequest, "bad_days", "天数 0–3650（0 为永久）")
		return
	}
	if in.Days > 0 {
		t := time.Now().AddDate(0, 0, in.Days)
		rule.ExpiresAt = &t
	}
	if err := s.store.PutRiskRule(r.Context(), rule); err != nil {
		s.internal(w, err)
		return
	}
	s.riskChanged(r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})
}

func (s *Server) adminRiskDeleteRule(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := s.store.DeleteRiskRule(r.Context(), q.Get("kind"), q.Get("value")); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	s.riskChanged(r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// riskChanged applies rule edits immediately: API checks, node user lists
// (blocked accounts drop out; nodes pick up blocked IPs on their next sync).
func (s *Server) riskChanged(r *http.Request) {
	_ = s.loadRisk(r.Context())
	s.nodeUsers.invalidate()
	s.invalidateSegments()
}

func (s *Server) adminRiskConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.cat().Risk)
}

func (s *Server) adminRiskPutConfig(w http.ResponseWriter, r *http.Request) {
	var in config.RiskConfig
	if !decode(w, r, &in) {
		return
	}
	next, err := s.live.Update(func(c *config.Catalog) error {
		c.Risk = in
		return nil
	}, s.saveCatalog(r))
	if s.catalogErr(w, err) {
		return
	}
	s.invalidateSegments()
	writeJSON(w, http.StatusOK, next.Risk)
}

// adminRiskGeo looks up one IP's location with the external service. Only on
// an explicit admin click: user IPs are never sent out automatically.
func (s *Server) adminRiskGeo(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if _, err := netip.ParseAddr(ip); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_ip", "IP 格式不正确")
		return
	}
	loc, err := geoip.Lookup(r.Context(), ip)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "geo_failed", "归属地查询失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, loc)
}

// ---- segments ----

func (s *Server) adminSegments(w http.ResponseWriter, r *http.Request) {
	rep, _, err := s.segments(r.Context(), r.URL.Query().Get("refresh") == "1")
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

type segUserView struct {
	ID            int64      `json:"id"`
	RechargeCode  string     `json:"recharge_code"`
	Tier          string     `json:"tier"`
	Tags          []string   `json:"tags"`
	PlanID        string     `json:"plan_id"`
	ExpiresAt     *time.Time `json:"expires_at"`
	Platform      string     `json:"platform"`
	CreatedAt     time.Time  `json:"created_at"`
	LTVCents      int64      `json:"ltv_cents"`
	PaidOrders    int        `json:"paid_orders"`
	MaxOrderCents int64      `json:"max_order_cents"`
	LastPaidAt    *time.Time `json:"last_paid_at"`
	Bytes30       int64      `json:"bytes_30"`
	ActiveDays30  int        `json:"active_days_30"`
	LastActiveDay string     `json:"last_active_day"`
	Invites       int        `json:"invites"`
}

func segUser(us *userSeg) segUserView {
	f := us.Fact
	v := segUserView{
		ID: f.ID, RechargeCode: rechargeCode(f.ID), Tier: us.Tier, Tags: us.Tags, PlanID: f.PlanID,
		Platform: f.Platform, CreatedAt: time.Unix(f.CreatedAt, 0), LTVCents: f.LTVCents, PaidOrders: f.PaidOrders,
		MaxOrderCents: f.MaxOrderCents, Bytes30: f.Bytes30, ActiveDays30: f.ActiveDays30,
		LastActiveDay: f.LastActiveDay, Invites: f.Invites,
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	if f.ExpiresAt > 0 {
		t := time.Unix(f.ExpiresAt, 0)
		v.ExpiresAt = &t
	}
	if f.LastPaidAt > 0 {
		t := time.Unix(f.LastPaidAt, 0)
		v.LastPaidAt = &t
	}
	return v
}

// segmentMembers returns the users of a segment sorted by sort ("ltv" default, "traffic", "recent", "new").
func (s *Server) segmentMembers(r *http.Request) ([]*userSeg, bool, error) {
	key := r.PathValue("key")
	if _, ok := segByKey[key]; !ok && key != "all" {
		return nil, false, nil
	}
	_, byID, err := s.segments(r.Context(), false)
	if err != nil {
		return nil, true, err
	}
	list := make([]*userSeg, 0, len(byID)/4)
	for _, us := range byID {
		if key == "all" || hasSeg(us, key) {
			list = append(list, us)
		}
	}
	less := map[string]func(a, b store.UserFact) bool{
		"ltv":     func(a, b store.UserFact) bool { return a.LTVCents > b.LTVCents },
		"traffic": func(a, b store.UserFact) bool { return a.Bytes30 > b.Bytes30 },
		"recent":  func(a, b store.UserFact) bool { return a.LastActiveDay > b.LastActiveDay },
		"new":     func(a, b store.UserFact) bool { return a.CreatedAt > b.CreatedAt },
	}[r.URL.Query().Get("sort")]
	if less == nil {
		less = func(a, b store.UserFact) bool { return a.LTVCents > b.LTVCents }
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i].Fact, list[j].Fact
		if less(a, b) != less(b, a) {
			return less(a, b)
		}
		return a.ID > b.ID
	})
	return list, true, nil
}

func (s *Server) adminSegmentUsers(w http.ResponseWriter, r *http.Request) {
	list, ok, err := s.segmentMembers(r)
	if err != nil {
		s.internal(w, err)
		return
	} else if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "分群不存在")
		return
	}
	limit, offset := pageParams(r)
	end := min(offset+limit, len(list))
	items := make([]segUserView, 0, max(end-offset, 0))
	for i := offset; i < end; i++ {
		items = append(items, segUser(list[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(list)})
}

func (s *Server) adminSegmentExport(w http.ResponseWriter, r *http.Request) {
	list, ok, err := s.segmentMembers(r)
	if err != nil {
		s.internal(w, err)
		return
	} else if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "分群不存在")
		return
	}
	name := r.PathValue("key")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="segment-%s-%s.csv"`, name, time.Now().Format("20060102")))
	w.Write([]byte("\xEF\xBB\xBF")) // BOM so Excel reads UTF-8
	cw := csv.NewWriter(w)
	label := func(k string) string { return segByKey[k].Label }
	_ = cw.Write([]string{"充值ID", "价值分层", "行为标签", "当前套餐", "到期", "平台", "注册时间", "累计消费(元)", "付费次数",
		"最高单笔(元)", "最近付费", "近30天流量(MB)", "近30天活跃天数", "最近活跃", "邀请人数"})
	ts := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("2006-01-02 15:04")
	}
	for _, us := range list {
		v := segUser(us)
		tags := make([]string, len(v.Tags))
		for i, t := range v.Tags {
			tags[i] = label(t)
		}
		_ = cw.Write([]string{v.RechargeCode, label(v.Tier), strings.Join(tags, "/"), v.PlanID, ts(v.ExpiresAt), v.Platform,
			v.CreatedAt.Format("2006-01-02 15:04"), fmt.Sprintf("%.2f", float64(v.LTVCents)/100), strconv.Itoa(v.PaidOrders),
			fmt.Sprintf("%.2f", float64(v.MaxOrderCents)/100), ts(v.LastPaidAt), strconv.FormatInt(v.Bytes30>>20, 10),
			strconv.Itoa(v.ActiveDays30), v.LastActiveDay, strconv.Itoa(v.Invites)})
	}
	cw.Flush()
}

func (s *Server) adminSegmentConfig(w http.ResponseWriter, r *http.Request) {
	var in config.SegmentConfig
	if !decode(w, r, &in) {
		return
	}
	next, err := s.live.Update(func(c *config.Catalog) error {
		c.Segments = in
		return nil
	}, s.saveCatalog(r))
	if s.catalogErr(w, err) {
		return
	}
	s.invalidateSegments()
	writeJSON(w, http.StatusOK, next.Segments)
}
