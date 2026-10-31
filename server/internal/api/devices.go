package api

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/store"
)

// Multi-device sharing ("一个订阅多台设备").
//
// The owner shows a link code (text + QR). Another device enters or scans it
// and becomes a member: it follows the owner's plan and shares its traffic
// quota, but keeps its own VLESS credential, so the owner can remove it at any
// time without affecting other devices. Only paid plans of at least
// multi_device.min_plan_days (default 30 = 月度会员及以上) may share, up to
// multi_device.max_devices devices in total (default 5, owner included).

const linkCodeTTL = 10 * time.Minute

// planHolder returns the account whose plan u uses: the owner for members,
// otherwise u itself. A member whose owner was deleted falls back to itself.
func (s *Server) planHolder(ctx context.Context, u *store.User) *store.User {
	if !u.Linked() {
		return u
	}
	owner, err := s.store.UserByID(ctx, u.PrimaryID)
	if err != nil {
		return u
	}
	return owner
}

// entitlement is entitlementOf applied to the plan holder; the device's own
// ban always wins.
func (s *Server) entitlement(ctx context.Context, u *store.User) Entitlement {
	holder := s.planHolder(ctx, u)
	e := entitlementOf(s.cat(), holder, time.Now())
	if holder.ID != u.ID && u.Disabled {
		e.CanConnect, e.Reason = false, "账号已被停用"
	}
	return e
}

// shareEligible reports whether owner's plan may be shared, with a reason if not.
func shareEligible(cat *config.Catalog, owner *store.User, now time.Time) (bool, string) {
	min := cat.MultiDevice.MinPlanDays
	if !owner.PaidPlanActive(now) {
		return false, fmt.Sprintf("多设备共享需要月度会员及以上套餐（有效期 ≥ %d 天）", min)
	}
	p := cat.Plan(owner.PlanID)
	if p == nil || p.DurationDays < min {
		return false, fmt.Sprintf("当前套餐不支持多设备，请升级到月度会员及以上（有效期 ≥ %d 天）", min)
	}
	return true, ""
}

type deviceView struct {
	ID           int64     `json:"id"`
	RechargeCode string    `json:"recharge_code"`
	Platform     string    `json:"platform"`
	CreatedAt    time.Time `json:"created_at"`
	Self         bool      `json:"self"`
	Owner        bool      `json:"owner"`
}

func devView(u *store.User, self, owner bool) deviceView {
	return deviceView{u.ID, rechargeCode(u.ID), u.Platform, u.CreatedAt, self, owner}
}

func (s *Server) linkURL(code string) string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	return s.cfg.PublicURL + "/l/" + code
}

// handleDevices: GET /api/v1/devices — the subscription's device list and sharing state.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request, u *store.User) {
	ctx := r.Context()
	cat := s.cat()
	owner := s.planHolder(ctx, u)
	members, err := s.store.Members(ctx, owner.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	devices := []deviceView{devView(owner, owner.ID == u.ID, true)}
	for _, m := range members {
		devices = append(devices, devView(m, m.ID == u.ID, false))
	}
	ok, reason := shareEligible(cat, owner, time.Now())
	out := map[string]any{
		"role":        map[bool]string{true: "member", false: "owner"}[u.Linked() && owner.ID != u.ID],
		"eligible":    ok,
		"reason":      reason,
		"max_devices": cat.MultiDevice.MaxDevices,
		"devices":     devices,
		"owner_code":  rechargeCode(owner.ID),
	}
	if owner.ID == u.ID && ok {
		if code, exp, has := s.store.ActiveLinkCode(ctx, u.ID); has {
			out["code"], out["code_display"], out["code_expires_at"], out["link"] =
				code, store.FormatLinkCode(code), exp, s.linkURL(code)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeviceCode: POST /api/v1/devices/code — create a fresh link code (owner only).
func (s *Server) handleDeviceCode(w http.ResponseWriter, r *http.Request, u *store.User) {
	if u.Linked() {
		writeErr(w, http.StatusForbidden, "is_member", "当前设备使用的是其它账户的订阅，不能再分享")
		return
	}
	if ok, reason := shareEligible(s.cat(), u, time.Now()); !ok {
		writeErr(w, http.StatusPaymentRequired, "not_eligible", reason)
		return
	}
	code, exp, err := s.store.CreateLinkCode(r.Context(), u.ID, linkCodeTTL)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code": code, "code_display": store.FormatLinkCode(code), "expires_at": exp, "link": s.linkURL(code),
	})
}

// handleDeviceJoin: POST /api/v1/devices/join {code} — follow the code owner's subscription.
func (s *Server) handleDeviceJoin(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.limiter.allowIP("join:", s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "操作过于频繁，请稍后再试")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	ownerID, err := s.store.LinkCodeOwner(ctx, body.Code)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_code", "令牌无效或已过期，请让对方重新生成")
		return
	}
	owner, err := s.store.UserByID(ctx, ownerID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_code", "令牌无效或已过期")
		return
	}
	cat := s.cat()
	if ok, reason := shareEligible(cat, owner, time.Now()); !ok {
		writeErr(w, http.StatusPaymentRequired, "not_eligible", "对方的"+reason)
		return
	}
	err = s.store.LinkDevice(ctx, u.ID, owner.ID, cat.MultiDevice.MaxDevices-1)
	switch {
	case errors.Is(err, store.ErrSelfLink):
		writeErr(w, http.StatusBadRequest, "self", "不能绑定自己的令牌")
		return
	case errors.Is(err, store.ErrDeviceLimit):
		writeErr(w, http.StatusConflict, "device_limit",
			fmt.Sprintf("该订阅已绑定 %d 台设备（上限），请让对方先移除不用的设备", cat.MultiDevice.MaxDevices))
		return
	case errors.Is(err, store.ErrHasMembers):
		writeErr(w, http.StatusConflict, "has_members", "本机已经在分享自己的订阅，请先移除本机绑定的其它设备")
		return
	case errors.Is(err, store.ErrPrimaryIsMem):
		writeErr(w, http.StatusBadRequest, "bad_code", "令牌无效")
		return
	case err != nil:
		s.internal(w, err)
		return
	}
	fresh, err := s.store.UserByID(ctx, u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.viewOf(ctx, fresh)})
}

// handleDeviceLeave: POST /api/v1/devices/leave — stop following another account.
func (s *Server) handleDeviceLeave(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !u.Linked() {
		writeErr(w, http.StatusBadRequest, "not_member", "当前设备没有绑定其它账户")
		return
	}
	if err := s.store.UnlinkDevice(r.Context(), u.ID); err != nil {
		s.internal(w, err)
		return
	}
	u.PrimaryID = 0
	writeJSON(w, http.StatusOK, map[string]any{"user": s.viewOf(r.Context(), u)})
}

// handleDeviceRemove: DELETE /api/v1/devices/{id} — owner removes a member.
func (s *Server) handleDeviceRemove(w http.ResponseWriter, r *http.Request, u *store.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "无效的设备")
		return
	}
	if err := s.store.RevokeMember(r.Context(), u.ID, id); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", "设备不存在或不属于你的订阅")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleLinkLanding: GET /l/{code} — what a phone camera opens when it scans the
// link QR outside the app: explains how to use the code inside the app.
func (s *Server) handleLinkLanding(w http.ResponseWriter, r *http.Request) {
	code := store.NormalizeLinkCode(r.PathValue("code"))
	if len(code) != 8 {
		code = ""
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = linkTmpl.Execute(w, map[string]string{"App": AppName, "Code": store.FormatLinkCode(code), "Android": s.androidURL()})
}

var linkTmpl = template.Must(template.New("link").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.App}} · 绑定设备</title>
<style>body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0d0f13;color:#f2f1ed;
font-family:-apple-system,"PingFang SC",sans-serif;padding:24px;box-sizing:border-box}.c{max-width:360px;text-align:center}
h1{font-size:24px;letter-spacing:3px}.code{margin:22px 0;padding:18px;border-radius:16px;background:rgba(255,255,255,.07);
font:800 30px ui-monospace,Menlo,monospace;letter-spacing:4px}p{color:#a9a8a3;font-size:14px;line-height:1.8}
a{display:block;margin-top:18px;padding:15px;border-radius:14px;background:#f2f1ed;color:#0d0f13;text-decoration:none;font-weight:700}</style>
</head><body><div class="c"><h1>{{.App}} · 绑定设备</h1>
{{if .Code}}<div class="code">{{.Code}}</div>{{end}}
<p>在新设备上打开「{{.App}}」→ 我的 → 多设备，<br>点「扫码绑定」扫描二维码，或输入上面的令牌。<br>令牌 10 分钟内有效。</p>
{{if .Android}}<a href="{{.Android}}">还没安装？下载 {{.App}}</a>{{end}}
</div></body></html>`))
