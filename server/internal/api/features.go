package api

import (
	"errors"
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
	if !s.limiter.allowIP("bind:", s.clientIP(r)) {
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

// handleApps returns the backend-configured recommended apps. On the iOS App
// Review face it returns none: Apple rejects an app that promotes or links out
// to other apps (a "mini App Store"), so the tab is empty there and the app
// hides it. Once approved the admin flips the review build off and iOS sees
// the list like everyone else.
func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if s.cat().Payment.IOSIAPFace(clientPlatform(r), clientBuild(r)) {
		writeJSON(w, http.StatusOK, map[string]any{"apps": []appView{}})
		return
	}
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
	allowSlowDownload(w)
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
