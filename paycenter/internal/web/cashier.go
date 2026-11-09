package web

import (
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"paycenter/internal/service"
	"paycenter/internal/store"
)

type payPage struct {
	Title    string
	Err      string
	Order    *store.Order
	Token    string
	Status   string // PENDING | PAID | CLOSED
	PayTypes []store.PayType
	Return   string
	// QR code page
	QR      template.URL // data: URI of the PNG
	QRLink  string
	PayName string
	// Form / scheme redirect page
	FormURL string
	Fields  map[string]string
	Scheme  template.URL // app link (alipays://, weixin://), checked before use
}

func (s *Server) payError(w http.ResponseWriter, err error) {
	status, msg := http.StatusInternalServerError, "系统繁忙，请稍后再试"
	var ue *service.UserError
	if errors.As(err, &ue) {
		status, msg = ue.Status, ue.Msg
	} else {
		slog.Error("cashier", "err", err)
	}
	// Cloudflare replaces an origin's 502 / 504 with its own "Bad gateway"
	// page, so the user would never see our message.
	if status == http.StatusBadGateway || status == http.StatusGatewayTimeout {
		status = http.StatusOK
	}
	s.render(w, status, "pay_message", payPage{Title: "支付", Err: msg})
}

// checkout: GET /checkout?<signed params> (the "下单链接" the VPN app opens).
func (s *Server) checkout(w http.ResponseWriter, r *http.Request) {
	o, err := s.Svc.Checkout(r.URL.Query(), s.clientIP(r))
	if err != nil {
		s.payError(w, err)
		return
	}
	http.Redirect(w, r, "/pay/"+o.TradeNo+"?t="+s.Svc.PageToken(o.TradeNo), http.StatusFound)
}

// pageOrder loads the order of a cashier URL and checks its token.
func (s *Server) pageOrder(w http.ResponseWriter, r *http.Request) (*store.Order, string, bool) {
	no, tok := r.PathValue("no"), r.FormValue("t")
	if !s.Svc.CheckPageToken(no, tok) {
		s.render(w, http.StatusForbidden, "pay_message", payPage{Title: "支付", Err: "链接无效"})
		return nil, "", false
	}
	o, err := s.Svc.OrderByTradeNo(no)
	if err != nil {
		s.render(w, http.StatusNotFound, "pay_message", payPage{Title: "支付", Err: "订单不存在"})
		return nil, "", false
	}
	return o, tok, true
}

func (s *Server) cashierData(o *store.Order, tok string) payPage {
	p := payPage{Title: "收银台", Order: o, Token: tok, Status: o.VPNStatus(time.Now()), Return: o.ReturnURL}
	if p.Status == "PENDING" {
		types, err := s.Svc.PayTypes(o)
		if err != nil {
			p.Err = err.Error()
		}
		p.PayTypes = types
	}
	return p
}

// cashier: GET /pay/{no}?t=
func (s *Server) cashier(w http.ResponseWriter, r *http.Request) {
	o, tok, ok := s.pageOrder(w, r)
	if !ok {
		return
	}
	s.render(w, http.StatusOK, "pay_cashier", s.cashierData(o, tok))
}

func isMobile(ua string) bool {
	ua = strings.ToLower(ua)
	for _, k := range []string{"mobile", "android", "iphone", "ipad", "micromessenger", "alipayclient"} {
		if strings.Contains(ua, k) {
			return true
		}
	}
	return false
}

// submit: POST /pay/{no}/submit (t, pay_type) — create the upstream order and go pay.
func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	o, tok, ok := s.pageOrder(w, r)
	if !ok {
		return
	}
	device := "pc"
	if isMobile(r.UserAgent()) {
		device = "wap"
	}
	payType := r.FormValue("pay_type")
	_, act, err := s.Svc.StartPay(r.Context(), o, payType, device)
	if err != nil {
		var ue *service.UserError
		if errors.As(err, &ue) && (ue.Status == http.StatusConflict || ue.Status == http.StatusGone) {
			http.Redirect(w, r, "/pay/"+o.TradeNo+"?t="+tok, http.StatusSeeOther)
			return
		}
		s.payError(w, err)
		return
	}
	name := payType
	if types, err := s.Svc.PayTypes(o); err == nil {
		for _, t := range types {
			if t.Code == payType {
				name = t.Name
			}
		}
	}
	base := payPage{Title: "支付", Order: o, Token: tok, Status: "PENDING", PayName: name, Return: o.ReturnURL}
	switch act.Kind {
	case "redirect":
		u, err := url.Parse(act.URL)
		if err != nil || u.Scheme == "" {
			s.payError(w, errors.New("bad pay url "+act.URL))
			return
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https":
			http.Redirect(w, r, act.URL, http.StatusSeeOther)
		case "javascript", "data", "vbscript", "file":
			s.payError(w, errors.New("refused pay url scheme "+u.Scheme))
		default: // app scheme such as alipays:// or weixin://
			base.Scheme = template.URL(act.URL)
			s.render(w, http.StatusOK, "pay_open", base)
		}
	case "qrcode":
		png, err := qrcode.Encode(act.QR, qrcode.Medium, 280)
		if err != nil {
			s.payError(w, err)
			return
		}
		base.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		base.QRLink = act.URL
		s.render(w, http.StatusOK, "pay_qrcode", base)
	case "form":
		if u, err := url.Parse(act.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			s.payError(w, errors.New("bad form action "+act.URL))
			return
		}
		base.FormURL, base.Fields = act.URL, act.Fields
		s.render(w, http.StatusOK, "pay_form", base)
	case "html":
		// The platform's own page (usually an auto-submitting form). Sandboxed
		// without allow-same-origin so its scripts can't act as this site.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "sandbox allow-forms allow-scripts allow-popups allow-top-navigation allow-top-navigation-by-user-activation")
		_, _ = io.WriteString(w, act.HTML)
	default:
		s.payError(w, errors.New("unknown action"))
	}
}

// status: GET /pay/{no}/status?t= (polled by the cashier pages).
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	no, tok := r.PathValue("no"), r.FormValue("t")
	if !s.Svc.CheckPageToken(no, tok) {
		writeJSON(w, http.StatusForbidden, map[string]string{"status": "FORBIDDEN"})
		return
	}
	o, err := s.Svc.OrderByTradeNo(no)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"status": "NOT_FOUND"})
		return
	}
	s.Svc.SyncIfStale(r.Context(), o, 5*time.Second)
	writeJSON(w, http.StatusOK, map[string]string{"status": o.VPNStatus(time.Now())})
}

// payReturn: GET /pay/{no}/return?t= (the platform's return_url). Refresh
// the status, then go back to the merchant if paid.
func (s *Server) payReturn(w http.ResponseWriter, r *http.Request) {
	o, tok, ok := s.pageOrder(w, r)
	if !ok {
		return
	}
	s.Svc.SyncIfStale(r.Context(), o, 2*time.Second)
	if o.Status == store.OrderPaid && o.ReturnURL != "" {
		http.Redirect(w, r, o.ReturnURL, http.StatusFound)
		return
	}
	s.render(w, http.StatusOK, "pay_cashier", s.cashierData(o, tok))
}

// upstreamNotify: 信联pay's callback; reply "success" or "fail".
func (s *Server) upstreamNotify(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "fail", http.StatusBadRequest)
		return
	}
	id, _ := strconv.ParseUint(r.PathValue("id"), 10, 64)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := s.Svc.HandleUpstreamNotify(uint(id), r.Form); err != nil {
		slog.Warn("upstream notify rejected", "channel", id, "ip", s.clientIP(r), "out_trade_no", r.Form.Get("out_trade_no"), "err", err)
		_, _ = io.WriteString(w, "fail")
		return
	}
	_, _ = io.WriteString(w, "success")
}

// vpnQuery: POST /api/order/query (sealed envelope from the VPN system).
func (s *Server) vpnQuery(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"code": "BAD_REQUEST", "message": "read body"})
		return
	}
	rep := s.Svc.HandleVPNQuery(r.Context(), body, s.clientIP(r))
	writeJSON(w, rep.Status, rep.Body)
}
