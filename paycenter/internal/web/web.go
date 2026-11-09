// Package web serves the cashier, the protocol endpoints and the admin panel.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"paycenter/internal/service"
	"paycenter/internal/store"
	"paycenter/internal/xinlian"
)

//go:embed templates/*.html static/*
var assets embed.FS

// Server holds the HTTP handlers.
type Server struct {
	Svc        *service.Service
	TrustProxy bool // read the client IP from X-Real-IP / X-Forwarded-For

	pages   map[string]*template.Template
	started time.Time

	failMu sync.Mutex
	fails  map[string]*loginFails
}

func New(svc *service.Service, trustProxy bool) (*Server, error) {
	s := &Server{Svc: svc, TrustProxy: trustProxy, fails: map[string]*loginFails{}, started: time.Now()}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

var funcs = template.FuncMap{
	"yuan": xinlian.CentsToYuan,
	"ts": func(v any) string {
		switch t := v.(type) {
		case time.Time:
			if t.IsZero() {
				return "-"
			}
			return t.Local().Format("2006-01-02 15:04:05")
		case *time.Time:
			if t == nil || t.IsZero() {
				return "-"
			}
			return t.Local().Format("2006-01-02 15:04:05")
		case int64:
			if t == 0 {
				return "-"
			}
			return time.Unix(t, 0).Local().Format("2006-01-02 15:04:05")
		}
		return "-"
	},
	"mask": func(s string) string {
		if s == "" {
			return "未设置"
		}
		r := []rune(s)
		if len(r) <= 8 {
			return "已设置"
		}
		return string(r[:4]) + "…" + string(r[len(r)-4:])
	},
	"orderCode": orderCode,
	"orderStatus": func(v any) string {
		return map[string]string{"PAID": "已支付", "CLOSED": "已过期", "PENDING": "待支付"}[orderCode(v)]
	},
	"notifyStatus": func(s string) string {
		return map[string]string{store.NotifyPending: "待通知", store.NotifySuccess: "已通知",
			store.NotifyFailed: "通知失败"}[s]
	},
	"attemptStatus": func(s string) string {
		return map[string]string{store.AttemptPending: "待支付", store.AttemptPaid: "已支付",
			store.AttemptClosed: "已关闭", store.AttemptFailed: "下单失败"}[s]
	},
	"statusTone": func(code string) string {
		return map[string]string{"PAID": "b-ok", "PENDING": "b-warn", "CLOSED": "b-muted"}[code]
	},
	"notifyLabel": func(s string) string {
		if v, ok := map[string]string{store.NotifyPending: "待通知", store.NotifySuccess: "已通知", store.NotifyFailed: "通知失败"}[s]; ok {
			return v
		}
		return "—"
	},
	"notifyTone": func(s string) string {
		if v, ok := map[string]string{store.NotifyPending: "b-info", store.NotifySuccess: "b-ok", store.NotifyFailed: "b-bad"}[s]; ok {
			return v
		}
		return "b-muted plain"
	},
	"attemptTone": func(s string) string {
		return map[string]string{store.AttemptPending: "b-warn", store.AttemptPaid: "b-ok",
			store.AttemptClosed: "b-muted", store.AttemptFailed: "b-bad"}[s]
	},
	"payName": payName,
	"logLabel": func(k string) string {
		if v, ok := logKinds[k]; ok {
			return v[0]
		}
		return k
	},
	"logTone": func(k string) string {
		if v, ok := logKinds[k]; ok {
			return v[1]
		}
		return ""
	},
	"initial": func(s string) string {
		r := []rune(strings.TrimSpace(s))
		if len(r) == 0 {
			return "?"
		}
		return strings.ToUpper(string(r[0]))
	},
	"short": func(s string, n int) string {
		if t := store.Trunc(s, n); t != s {
			return t + "…"
		}
		return s
	},
	"add": func(a, b int) int { return a + b },
	"host": func(u string) string {
		u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		h, _, _ := strings.Cut(u, "/")
		return h
	},
}

func payName(code string) string {
	if code == "" {
		return "—"
	}
	if v, ok := map[string]string{"alipay": "支付宝", "wxpay": "微信支付", "qqpay": "QQ 钱包", "bank": "网银",
		"simulate": "模拟支付"}[code]; ok {
		return v
	}
	return code
}

// logKinds: order log kind -> label, timeline tone.
var logKinds = map[string][2]string{
	"checkout": {"收到下单链接", "pri"}, "submit": {"上游下单", "info"}, "submit_fail": {"上游下单失败", "bad"},
	"paid": {"支付成功", "ok"}, "notify_ok": {"通知 VPN 成功", "ok"}, "notify_fail": {"通知 VPN 失败", "warn"},
	"notify_giveup": {"停止通知", "bad"}, "renotify": {"手动重发通知", "info"}, "close": {"关闭上游订单", ""},
	"duplicate": {"重复支付", "bad"}, "notify_reject": {"上游回调被拒", "bad"}, "query_reject": {"查单结果被拒", "bad"},
}

func orderCode(v any) string {
	switch o := v.(type) {
	case store.Order:
		return o.VPNStatus(time.Now())
	case *store.Order:
		return o.VPNStatus(time.Now())
	}
	return ""
}

// loadTemplates parses each page together with its layout.
func (s *Server) loadTemplates() error {
	s.pages = map[string]*template.Template{}
	names, err := fs.Glob(assets, "templates/*.html")
	if err != nil {
		return err
	}
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if strings.HasPrefix(base, "_") {
			continue
		}
		files := []string{"templates/_admin.html", n}
		switch {
		case strings.HasPrefix(base, "pay_"):
			files = []string{"templates/_pay.html", n}
		case base == "login": // has its own layout
			files = []string{n}
		}
		t, err := template.New(base).Funcs(funcs).ParseFS(assets, files...)
		if err != nil {
			return fmt.Errorf("template %s: %w", n, err)
		}
		s.pages[base] = t
	}
	return nil
}

func (s *Server) render(w http.ResponseWriter, status int, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("render", "page", page, "err", err)
		http.Error(w, "页面渲染失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// clientIP is the peer address, or the proxy-reported one when trusted.
func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
			return v
		}
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			parts := strings.Split(v, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/admin") {
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// Handler is the whole HTTP surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	// VPN system protocol.
	mux.HandleFunc("GET /checkout", s.checkout)
	mux.HandleFunc("POST /api/order/query", s.vpnQuery)
	// Cashier pages.
	mux.HandleFunc("GET /pay/{no}", s.cashier)
	mux.HandleFunc("POST /pay/{no}/submit", s.submit)
	mux.HandleFunc("GET /pay/{no}/status", s.status)
	mux.HandleFunc("GET /pay/{no}/return", s.payReturn)
	// 信联pay callback.
	mux.HandleFunc("POST /notify/xinlian/{id}", s.upstreamNotify)
	mux.HandleFunc("GET /notify/xinlian/{id}", s.upstreamNotify)
	// Built-in "模拟 VPN" notify receiver (test orders are normally delivered
	// in-process; this is the same receiver over HTTP).
	mux.HandleFunc("POST "+service.TestNotifyPath, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		st, rep := s.Svc.ReceiveTestNotify(body)
		writeJSON(w, st, rep)
	})

	s.adminRoutes(mux)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusFound)
	})
	return secureHeaders(mux)
}
