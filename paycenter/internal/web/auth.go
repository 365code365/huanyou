package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"paycenter/internal/store"
)

const (
	sessionCookie   = "pc_admin"
	sessionTTL      = 12 * time.Hour
	settingUser     = "admin_user"
	settingPassHash = "admin_password_hash"
	settingSession  = "session_secret" // rotated on password change: logs everyone out
	maxLoginFails   = 8
	loginFailWindow = 15 * time.Minute
)

type loginFails struct {
	n     int
	first time.Time
}

// EnsureAdmin creates the admin account on first start. If password is
// empty a random one is generated and logged once.
func EnsureAdmin(db *gorm.DB, user, password string) (created bool, pw string, err error) {
	if store.GetSetting(db, settingPassHash) != "" {
		return false, "", nil
	}
	if user == "" {
		user = "admin"
	}
	if password == "" {
		b := make([]byte, 9)
		_, _ = rand.Read(b)
		password = hex.EncodeToString(b)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return false, "", err
	}
	if err := store.SetSetting(db, settingUser, user); err != nil {
		return false, "", err
	}
	if err := store.SetSetting(db, settingPassHash, string(h)); err != nil {
		return false, "", err
	}
	return true, password, nil
}

// SetAdminPassword sets a new password and logs out every session.
func SetAdminPassword(db *gorm.DB, password string) error {
	if len(password) < 8 {
		return errors.New("密码至少 8 位")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := store.SetSetting(db, settingPassHash, string(h)); err != nil {
		return err
	}
	return store.SetSetting(db, settingSession, "")
}

func (s *Server) sessionSecret() []byte { return s.Svc.Secret(settingSession) }

func (s *Server) mac(parts ...string) string {
	m := hmac.New(sha256.New, s.sessionSecret())
	m.Write([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) newSession(user string) string {
	exp := strconv.FormatInt(time.Now().Add(sessionTTL).Unix(), 10)
	return url.QueryEscape(user) + "." + exp + "." + s.mac("session", user, exp)
}

// session returns the logged-in user and the raw cookie value.
func (s *Server) session(r *http.Request) (string, string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return "", "", false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return "", "", false
	}
	user, err := url.QueryUnescape(parts[0])
	if err != nil {
		return "", "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", "", false
	}
	if !hmac.Equal([]byte(s.mac("session", user, parts[1])), []byte(parts[2])) {
		return "", "", false
	}
	return user, c.Value, true
}

func (s *Server) csrfToken(cookie string) string { return s.mac("csrf", cookie)[:32] }

func (s *Server) secureCookie(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https" {
		return true
	}
	return strings.HasPrefix(s.Svc.PublicURL(), "https://")
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: value, Path: "/admin", MaxAge: maxAge,
		HttpOnly: true, Secure: s.secureCookie(r), SameSite: http.SameSiteStrictMode})
}

// sameOrigin rejects cross-site form posts (defense in depth next to the
// CSRF token and the SameSite cookie).
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// adminCtx is passed to admin templates.
type adminCtx struct {
	User      string
	CSRF      string
	Nav       string
	Ok        string
	Err       string
	DB        string // sqlite | mysql
	PublicURL string
	Badge     int64 // failed VPN notifications (sidebar)
}

// requireAdmin wraps an admin handler with session + CSRF checks.
func (s *Server) requireAdmin(nav string, h func(http.ResponseWriter, *http.Request, adminCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, raw, ok := s.session(r)
		if !ok {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/admin/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
			} else {
				http.Error(w, "请先登录", http.StatusUnauthorized)
			}
			return
		}
		ctx := adminCtx{User: user, CSRF: s.csrfToken(raw), Nav: nav, Ok: r.URL.Query().Get("ok"), Err: r.URL.Query().Get("err")}
		if r.Method != http.MethodGet {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if !sameOrigin(r) || !hmac.Equal([]byte(r.FormValue("_csrf")), []byte(ctx.CSRF)) {
				http.Error(w, "请求已失效，请刷新页面后重试", http.StatusForbidden)
				return
			}
		}
		ctx.DB, ctx.PublicURL = s.Svc.DB.Dialector.Name(), s.Svc.PublicURL()
		if r.Method == http.MethodGet {
			s.Svc.DB.Model(&store.Order{}).Where("notify_status = ? AND is_test = ?", store.NotifyFailed, false).Count(&ctx.Badge)
		}
		h(w, r, ctx)
	}
}

func (s *Server) loginBlocked(ip string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	f := s.fails[ip]
	if f == nil {
		return false
	}
	if time.Since(f.first) > loginFailWindow {
		delete(s.fails, ip)
		return false
	}
	return f.n >= maxLoginFails
}

func (s *Server) loginFailed(ip string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	f := s.fails[ip]
	if f == nil || time.Since(f.first) > loginFailWindow {
		f = &loginFails{first: time.Now()}
		s.fails[ip] = f
	}
	f.n++
	if len(s.fails) > 10000 {
		s.fails = map[string]*loginFails{}
	}
}

func safeNext(next string) string {
	if strings.HasPrefix(next, "/admin") && !strings.HasPrefix(next, "//") && !strings.Contains(next, "\\") {
		return next
	}
	return "/admin/"
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.session(r); ok {
		http.Redirect(w, r, "/admin/", http.StatusFound)
		return
	}
	s.render(w, http.StatusOK, "login", map[string]any{"Next": r.URL.Query().Get("next")})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	ip := s.clientIP(r)
	next := safeNext(r.FormValue("next"))
	fail := func(msg string) {
		s.render(w, http.StatusUnauthorized, "login", map[string]any{"Next": next, "Err": msg, "User": r.FormValue("user")})
	}
	if !sameOrigin(r) {
		fail("请求来源无效")
		return
	}
	if s.loginBlocked(ip) {
		fail("尝试次数过多，请 15 分钟后再试")
		return
	}
	db := s.Svc.DB
	user, pass := strings.TrimSpace(r.FormValue("user")), r.FormValue("password")
	hash := store.GetSetting(db, settingPassHash)
	if user != store.GetSetting(db, settingUser) || bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil {
		s.loginFailed(ip)
		slog.Warn("admin login failed", "ip", ip, "user", user)
		fail("用户名或密码错误")
		return
	}
	slog.Info("admin login", "ip", ip, "user", user)
	s.setSession(w, r, s.newSession(user), int(sessionTTL/time.Second))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ adminCtx) {
	s.setSession(w, r, "", -1)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
