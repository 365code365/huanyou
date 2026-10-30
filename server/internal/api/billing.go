package api

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/payment"
	"vpnplatform/server/internal/store"
)

type planView struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	PriceCents    int64   `json:"price_cents"`
	DurationDays  int     `json:"duration_days"`
	TrafficGB     float64 `json:"traffic_gb"`
	SpeedMbps     int     `json:"speed_mbps"`
	OverQuotaKbps int     `json:"over_quota_kbps"`
	Free          bool    `json:"free"`
}

func (s *Server) handlePlans(w http.ResponseWriter, _ *http.Request) {
	cat := s.cat()
	list := cat.PlansByPrice()
	plans := make([]planView, 0, len(list))
	for _, p := range list {
		if p.Hidden {
			continue
		}
		plans = append(plans, planView{p.ID, p.Name, p.Description, p.PriceCents, p.DurationDays,
			p.TrafficGB, p.SpeedMbps, p.OverQuotaKbps, p.ID == cat.FreePlan})
	}
	methods := []payment.Method{}
	for _, p := range s.providers {
		methods = append(methods, p.Methods()...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"plans": plans, "methods": methods})
}

// providerFor maps a method id ("alipay", "epay.wxpay", "mock") to its provider.
func (s *Server) providerFor(method string) payment.Provider {
	for _, p := range s.providers {
		for _, m := range p.Methods() {
			if m.ID == method {
				return p
			}
		}
	}
	return nil
}

func (s *Server) handleCreateOrder(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		PlanID string `json:"plan_id"`
		Method string `json:"method"`
	}
	if !decode(w, r, &req) {
		return
	}
	plan := s.cat().Plan(req.PlanID)
	if plan == nil || plan.Hidden || plan.ID == s.cat().FreePlan {
		writeErr(w, http.StatusBadRequest, "bad_plan", "套餐不存在")
		return
	}
	prov := s.providerFor(req.Method)
	if prov == nil {
		writeErr(w, http.StatusBadRequest, "bad_method", "不支持的支付方式")
		return
	}
	if u.Disabled {
		writeErr(w, http.StatusForbidden, "disabled", "账号已被停用")
		return
	}
	o, err := s.store.CreateOrder(r.Context(), u.ID, plan.ID, plan.PriceCents, req.Method)
	if err != nil {
		s.internal(w, err)
		return
	}
	payURL, err := prov.PayURL(payment.Order{ID: o.ID, AmountCents: o.AmountCents, Subject: plan.Name},
		req.Method,
		s.cfg.PublicURL+"/pay/notify/"+prov.Name(),
		s.cfg.PublicURL+"/pay/return?order="+o.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"order": o, "pay_url": payURL})
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request, u *store.User) {
	o, err := s.store.OrderByID(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) || (err == nil && o.UserID != u.ID) {
		writeErr(w, http.StatusNotFound, "not_found", "订单不存在")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleListOrders(w http.ResponseWriter, r *http.Request, u *store.User) {
	orders, err := s.store.OrdersByUser(r.Context(), u.ID, 50)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": orders})
}

// handleNotify receives the asynchronous payment result from a provider.
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	var prov payment.Provider
	for _, p := range s.providers {
		if p.Name() == r.PathValue("provider") {
			prov = p
		}
	}
	if prov == nil {
		http.NotFound(w, r)
		return
	}
	n, err := prov.ParseNotify(r)
	if err != nil {
		slog.Warn("payment notify rejected", "provider", prov.Name(), "err", err)
		http.Error(w, "fail", http.StatusBadRequest)
		return
	}
	if !n.Paid { // e.g. WAIT_BUYER_PAY: acknowledge, nothing to do
		w.Write([]byte(prov.Ack()))
		return
	}
	if err := s.completeOrder(r, n); err != nil {
		slog.Error("payment notify failed", "provider", prov.Name(), "order", n.OrderID, "err", err)
		http.Error(w, "fail", http.StatusBadRequest) // provider will retry
		return
	}
	w.Write([]byte(prov.Ack()))
}

func (s *Server) completeOrder(r *http.Request, n *payment.Notification) error {
	o, err := s.store.OrderByID(r.Context(), n.OrderID)
	if err != nil {
		return err
	}
	if o.AmountCents != n.AmountCents {
		return fmt.Errorf("amount mismatch: order %d, paid %d", o.AmountCents, n.AmountCents)
	}
	days := 30
	if p := s.cat().Plan(o.PlanID); p != nil {
		days = p.DurationDays
	}
	granted, err := s.store.MarkPaid(r.Context(), o.ID, n.TradeNo, days, time.Now())
	if err != nil {
		return err
	}
	if granted {
		slog.Info("order paid", "order", o.ID, "user", o.UserID, "plan", o.PlanID, "amount", payment.Yuan(o.AmountCents))
	}
	return nil
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Title}}</title>
<style>body{font-family:-apple-system,sans-serif;max-width:420px;margin:15vh auto;padding:0 24px;text-align:center;color:#222}
button{font-size:18px;padding:14px 40px;border:0;border-radius:10px;background:#2f6bff;color:#fff}</style></head>
<body><h2>{{.Title}}</h2><p>{{.Body}}</p>
{{if .Form}}<form method="post" action="/pay/notify/mock">
<input type="hidden" name="order_id" value="{{.OrderID}}"><input type="hidden" name="t" value="{{.Token}}">
<input type="hidden" name="amount" value="{{.Amount}}"><button type="submit">确认支付 ¥{{.Amount}}</button></form>{{end}}
</body></html>`))

type page struct {
	Title, Body            string
	Form                   bool
	OrderID, Token, Amount string
}

// handleReturn is where the provider sends the user's browser after paying.
func (s *Server) handleReturn(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTmpl.Execute(w, page{Title: "支付已提交", Body: "请返回 App，套餐会在支付成功后自动生效。"})
}

// handleMockPay shows the fake cashier page (only when payments.mock is enabled).
func (s *Server) handleMockPay(w http.ResponseWriter, r *http.Request) {
	m := s.mock
	id := r.PathValue("id")
	o, err := s.store.OrderByID(r.Context(), id)
	if m == nil || err != nil || r.URL.Query().Get("t") != m.Token(id) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if o.Status == "paid" {
		pageTmpl.Execute(w, page{Title: "已支付", Body: "请返回 App。"})
		return
	}
	pageTmpl.Execute(w, page{Title: "模拟支付", Body: "订单 " + o.ID + "（测试环境，不会真实扣款）",
		Form: true, OrderID: o.ID, Token: m.Token(o.ID), Amount: payment.Yuan(o.AmountCents)})
}

func newProviders(cfg *config.API) ([]payment.Provider, *payment.Mock, error) {
	var out []payment.Provider
	if a := cfg.Payments.Alipay; a != nil {
		p, err := payment.NewAlipay(a.AppID, a.Gateway, a.PrivateKeyFile, a.AlipayPublicKeyFile)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, p)
	}
	if e := cfg.Payments.Epay; e != nil {
		out = append(out, &payment.Epay{APIURL: e.APIURL, PID: e.PID, Key: e.Key, Types: e.Types})
	}
	var mock *payment.Mock
	if cfg.Payments.Mock {
		slog.Warn("MOCK PAYMENTS ENABLED: anyone can get plans for free. Do not use in production.")
		mock = &payment.Mock{PublicURL: cfg.PublicURL, Secret: []byte("mock:" + cfg.JWTSecret)}
		out = append(out, mock)
	}
	return out, mock, nil
}

// handleTraffic receives per-user traffic deltas from a node.
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request) {
	node := s.authNode(r)
	if node == nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}
	var body struct {
		Traffic []struct {
			Email string `json:"email"`
			Up    int64  `json:"up"`
			Down  int64  `json:"down"`
		} `json:"traffic"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if !decodeBody(w, r, &body) {
		return
	}
	usage := map[int64]store.Traffic{}
	var nodeBytes int64
	for _, t := range body.Traffic {
		if t.Up > 0 {
			nodeBytes += t.Up
		}
		if t.Down > 0 {
			nodeBytes += t.Down
		}
		id, ok := store.ParseXrayEmail(strings.TrimSpace(t.Email))
		if !ok {
			continue
		}
		cur := usage[id]
		usage[id] = store.Traffic{Up: cur.Up + t.Up, Down: cur.Down + t.Down}
	}
	period := time.Duration(s.cat().TrafficPeriodDays) * 24 * time.Hour
	if err := s.store.AddTraffic(r.Context(), usage, time.Now(), period); err != nil {
		s.internal(w, err)
		return
	}
	if s.monitor != nil {
		s.monitor.AddTraffic(node.ID, nodeBytes, len(usage))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request, u *store.User) {
	since := time.Now().AddDate(0, 0, -29).Format("2006-01-02")
	days, err := s.store.Usage(r.Context(), u.ID, since)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": days})
}
