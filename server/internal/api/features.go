package api

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/store"
)

// AppName is the product name shown on the landing page and in share text.
const AppName = "环游"

// bindWindow is how long after account creation a user may still enter an invite code.
const bindWindow = 7 * 24 * time.Hour

// handleReferral returns the user's invite code, share links and stats.
func (s *Server) handleReferral(w http.ResponseWriter, r *http.Request, u *store.User) {
	ref := s.cat().Referral
	invited, err := s.store.CountInvited(r.Context(), u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	inviteURL, downloadURL := "", ""
	if s.cfg.PublicURL != "" {
		inviteURL = s.cfg.PublicURL + "/i/" + u.RefCode
		downloadURL = s.cfg.PublicURL + "/download"
	}
	slogan := s.cat().Share.Slogan
	if slogan == "" {
		slogan = "一键连接，畅游全球"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":       ref.Enabled,
		"code":          u.RefCode,
		"invite_url":    inviteURL,
		"download_url":  downloadURL,
		"share_text":    shareText(slogan, u.RefCode, inviteURL),
		"slogan":        slogan,
		"invited_count": invited,
		"inviter_days":  ref.InviterDays,
		"invitee_days":  ref.InviteeDays,
		"max_invites":   ref.MaxInvites,
		"invited":       u.InvitedBy != 0,
		"can_bind":      ref.Enabled && u.InvitedBy == 0 && time.Since(u.CreatedAt) < bindWindow,
	})
}

// shareText is what "复制邀请链接" puts on the clipboard. It contains the
// "环游邀请码 XXXX" marker the app looks for on first launch.
func shareText(slogan, code, url string) string {
	t := AppName + " · " + slogan + "\n" + AppName + "邀请码 " + code
	if url != "" {
		t += "\n下载：" + url
	}
	return t
}

// handleReferralBind lets a recently created account enter an inviter's code
// (device accounts never see a registration form, so this is their way in).
func (s *Server) handleReferralBind(w http.ResponseWriter, r *http.Request, u *store.User) {
	if !s.limiter.allow("bind:" + s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "操作过于频繁，请稍后再试")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	ref := s.cat().Referral
	code := strings.ToUpper(strings.TrimSpace(body.Code))
	switch {
	case !ref.Enabled:
		writeErr(w, http.StatusForbidden, "referral_disabled", "邀请活动暂未开启")
		return
	case u.InvitedBy != 0:
		writeErr(w, http.StatusConflict, "already_bound", "你已经填写过邀请码")
		return
	case time.Since(u.CreatedAt) >= bindWindow:
		writeErr(w, http.StatusForbidden, "bind_expired", "只有新用户 7 天内可以填写邀请码")
		return
	case code == "" || code == u.RefCode:
		writeErr(w, http.StatusBadRequest, "bad_code", "邀请码无效")
		return
	}
	inviter, err := s.store.UserByRefCode(r.Context(), code)
	if errors.Is(err, store.ErrNotFound) || (inviter != nil && inviter.InvitedBy == u.ID) {
		writeErr(w, http.StatusBadRequest, "bad_code", "邀请码无效")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	res, err := s.store.ApplyReferral(r.Context(), u.ID, inviter.ID,
		ref.RewardPlanID, ref.InviterDays, ref.InviteeDays, ref.MaxInvites, time.Now())
	if err != nil {
		slog.Warn("referral bind", "err", err)
		writeErr(w, http.StatusConflict, "already_bound", "你已经填写过邀请码")
		return
	}
	fresh, err := s.store.UserByID(r.Context(), u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invitee_days": res.InviteeDays, "user": s.viewOf(r.Context(), fresh)})
}

type appView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IconURL  string `json:"icon_url"`
	URL      string `json:"url"`
	Category string `json:"category"`
	Desc     string `json:"desc"`
}

// handleApps returns the backend-configured recommended apps.
func (s *Server) handleApps(w http.ResponseWriter, _ *http.Request) {
	list := append([]config.AppRec(nil), s.cat().Apps...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Sort < list[j].Sort })
	out := make([]appView, 0, len(list))
	for _, a := range list {
		if a.Disabled {
			continue
		}
		out = append(out, appView{a.ID, a.Name, a.IconURL, a.URL, a.Category, a.Desc})
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": out})
}

type contactView struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Group string `json:"group"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Link  string `json:"link"` // "" = the app copies Value
	Desc  string `json:"desc"`
}

// handleContacts returns enabled customer-service / social channels.
func (s *Server) handleContacts(w http.ResponseWriter, _ *http.Request) {
	list := append([]config.Contact(nil), s.cat().Contacts...)
	sort.SliceStable(list, func(i, j int) bool { return list[i].Sort < list[j].Sort })
	out := make([]contactView, 0, len(list))
	for i := range list {
		k := &list[i]
		if k.Disabled {
			continue
		}
		out = append(out, contactView{k.ID, k.Type, k.Group, k.Name, k.Value, k.Link(), k.Desc})
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": out})
}

// ---- download / invite landing page ----

var safeFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)

// handleDownloadFile serves files (the Android APK) from download_dir.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !safeFile.MatchString(name) || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.cfg.DownloadDir, name)
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(name, ".apk") {
		w.Header().Set("Content-Type", "application/vnd.android.package-archive")
		w.Header().Set("Content-Disposition", `attachment; filename="`+AppName+`.apk"; filename*=UTF-8''%E7%8E%AF%E6%B8%B8.apk`)
	}
	http.ServeFile(w, r, p)
}

// androidURL is the configured Android link, or the self-hosted APK if present.
func (s *Server) androidURL() string {
	if u := s.cat().Share.AndroidURL; u != "" {
		return u
	}
	if st, err := os.Stat(filepath.Join(s.cfg.DownloadDir, "app.apk")); err == nil && !st.IsDir() {
		return "/dl/app.apk"
	}
	return ""
}

var codePattern = regexp.MustCompile(`^[A-Z2-9]{7}$`)

// handleLanding renders the public download page; /i/{code} also carries an invite code.
func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(r.PathValue("code"))
	if !codePattern.MatchString(code) {
		code = ""
	}
	ref := s.cat().Referral
	slogan := s.cat().Share.Slogan
	if slogan == "" {
		slogan = "一键连接，畅游全球"
	}
	data := map[string]any{
		"App": AppName, "Slogan": slogan, "Code": code,
		"Android": s.androidURL(), "IOS": s.cat().Share.IOSURL,
		"InviteeDays": 0,
	}
	if ref.Enabled && code != "" {
		data["InviteeDays"] = ref.InviteeDays
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if err := landingTmpl.Execute(w, data); err != nil {
		slog.Warn("landing render", "err", err)
	}
}

var landingTmpl = template.Must(template.New("landing").Parse(`<!doctype html>
<html lang="zh-CN"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<title>{{.App}} · {{.Slogan}}</title>
<style>
*{box-sizing:border-box;margin:0}
body{min-height:100vh;font-family:-apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei",sans-serif;
 background:radial-gradient(900px 520px at 85% -10%,#1f4d3a 0%,transparent 60%),radial-gradient(700px 500px at -10% 110%,#1d2a44 0%,transparent 60%),#0d0f13;
 color:#f2f1ed;display:flex;align-items:center;justify-content:center;padding:32px 22px}
.card{width:100%;max-width:380px;text-align:center}
.mark{width:84px;height:84px;margin:0 auto 22px;filter:drop-shadow(0 18px 40px rgba(18,161,80,.25))}
h1{font-size:34px;font-weight:800;letter-spacing:4px}
.slogan{margin-top:8px;color:#a9a8a3;font-size:15px}
.gift{margin:28px 0 6px;padding:18px;border-radius:18px;background:rgba(255,255,255,.06);border:1px solid rgba(255,255,255,.09)}
.gift small{color:#a9a8a3;font-size:13px}
.code{margin-top:6px;font:800 30px ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:6px}
.gift p{margin-top:8px;color:#7fd8a4;font-size:13px}
.btn{display:flex;align-items:center;justify-content:center;gap:10px;height:54px;margin-top:14px;border-radius:15px;
 font-size:16px;font-weight:700;text-decoration:none;border:0;width:100%;cursor:pointer}
.primary{background:#f2f1ed;color:#0d0f13}.ghost{background:rgba(255,255,255,.08);color:#f2f1ed;border:1px solid rgba(255,255,255,.12)}
.disabled{opacity:.45;pointer-events:none}
.tip{margin-top:20px;color:#77767a;font-size:12px;line-height:1.7}
#toast{position:fixed;left:50%;bottom:40px;transform:translateX(-50%);background:#f2f1ed;color:#0d0f13;padding:10px 18px;border-radius:12px;font-size:14px;opacity:0;transition:.25s}
#wx{display:none;position:fixed;inset:0;background:rgba(0,0,0,.86);color:#fff;font-size:17px;line-height:1.8;text-align:right;padding:24px 28px}
</style></head><body>
<div class="card">
 <svg class="mark" viewBox="0 0 100 100"><rect width="100" height="100" rx="27" fill="#15171c" stroke="rgba(255,255,255,.12)"/>
  <path d="M 74.80 46.87 A 25 25 0 1 1 53.13 25.20" fill="none" stroke="#fff" stroke-width="9.5" stroke-linecap="round"/>
  <circle cx="67.68" cy="32.32" r="7.5" fill="#12a150"/></svg>
 <h1>{{.App}}</h1>
 <div class="slogan">{{.Slogan}}</div>
 {{if .Code}}<div class="gift"><small>好友邀请码</small><div class="code">{{.Code}}</div>
  {{if .InviteeDays}}<p>下载后自动领取 {{.InviteeDays}} 天会员</p>{{end}}</div>{{end}}
 {{if .Android}}<a class="btn primary" href="{{.Android}}" onclick="cp()">Android 下载</a>
 {{else}}<a class="btn primary disabled">Android 版即将上线</a>{{end}}
 {{if .IOS}}<a class="btn ghost" href="{{.IOS}}" onclick="cp()">iPhone 下载</a>
 {{else}}<a class="btn ghost disabled">iPhone 版即将上线</a>{{end}}
 <div class="tip">无需注册，打开即用{{if .Code}}<br>安装前请保持邀请码已复制，首次打开会自动识别{{end}}</div>
</div>
<div id="toast">邀请码已复制</div>
<div id="wx">点右上角 ··· <br>选择「在浏览器打开」<br>即可下载 ↗</div>
<script>
var code={{.Code}},app={{.App}};
function cp(){if(!code)return;var t=app+"邀请码 "+code;
 try{navigator.clipboard.writeText(t)}catch(e){var a=document.createElement("textarea");a.value=t;document.body.appendChild(a);a.select();document.execCommand("copy");a.remove()}
 var el=document.getElementById("toast");el.style.opacity=1;setTimeout(function(){el.style.opacity=0},1600)}
if(/MicroMessenger|QQ\//i.test(navigator.userAgent))document.getElementById("wx").style.display="block";
</script></body></html>`))
