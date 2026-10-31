package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/payment"
	"vpnplatform/server/internal/store"
)

// The "下单链接" payment gateway (an external payment center). The center
// calls our fixed notify endpoint; we call the center's query API.
const (
	gatewayMethod     = "gateway"
	gatewayNotifyPath = "/pay/gateway/notify"
)

type gatewayCache struct {
	mu  sync.Mutex
	key string
	gw  *payment.Gateway
}

// gateway returns the gateway for the current settings, or nil if it can't be
// built. The instance is kept while the settings don't change, so its nonce
// cache (replay protection) survives between requests.
func (s *Server) gateway() *payment.Gateway {
	g := s.cat().Payment.Gateway
	if g.MerchantID == "" || g.SignKey == "" || g.EncKey == "" {
		return nil
	}
	key := g.PayLink + "|" + g.QueryURL + "|" + g.MerchantID + "|" + g.SignKey + "|" + g.EncKey
	c := &s.gw
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == key {
		return c.gw
	}
	gw, err := payment.NewGateway(g.PayLink, g.QueryURL, g.MerchantID, g.SignKey, g.EncKey)
	if err != nil {
		slog.Warn("payment gateway config invalid", "err", err)
		return nil
	}
	c.key, c.gw = key, gw
	return gw
}

func (s *Server) gatewayAllowed(r *http.Request, g config.GatewayConfig) bool {
	if len(g.AllowIPs) == 0 {
		return true
	}
	ip := s.clientIP(r)
	for _, a := range g.AllowIPs {
		if p, err := parseIPRule(a); err == nil {
			if addr, err := parseIPRule(ip); err == nil && p.Contains(addr.Addr()) {
				return true
			}
		}
	}
	return false
}

// openGateway authenticates a call from the payment center.
func (s *Server) openGateway(w http.ResponseWriter, r *http.Request, v any) (*payment.Gateway, bool) {
	g := s.cat().Payment.Gateway
	gw := s.gateway()
	if gw == nil {
		gatewayFail(w, http.StatusServiceUnavailable, "NOT_CONFIGURED", "payment gateway not configured")
		return nil, false
	}
	if !s.gatewayAllowed(r, g) {
		slog.Warn("payment gateway call from a non-allowed IP", "ip", s.clientIP(r), "path", r.URL.Path)
		gatewayFail(w, http.StatusForbidden, "IP_DENIED", "ip not allowed")
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		gatewayFail(w, http.StatusBadRequest, "BAD_REQUEST", "read body")
		return nil, false
	}
	var env payment.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		gatewayFail(w, http.StatusBadRequest, "BAD_REQUEST", "body must be a JSON envelope")
		return nil, false
	}
	if err := gw.Open(&env, v); err != nil {
		slog.Warn("payment gateway envelope rejected", "ip", s.clientIP(r), "path", r.URL.Path, "err", err)
		code := "BAD_ENVELOPE"
		if errors.Is(err, payment.ErrBadSignature) {
			code = "BAD_SIGN"
		}
		gatewayFail(w, http.StatusUnauthorized, code, err.Error())
		return nil, false
	}
	return gw, true
}

func gatewayFail(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"code": code, "message": msg})
}

// handleGatewayNotify: the payment center reports a result.
func (s *Server) handleGatewayNotify(w http.ResponseWriter, r *http.Request) {
	var o payment.GatewayOrder
	if _, ok := s.openGateway(w, r, &o); !ok {
		return
	}
	if o.OutTradeNo == "" || (o.Currency != "" && o.Currency != "CNY") {
		gatewayFail(w, http.StatusBadRequest, "BAD_ORDER", "missing out_trade_no or currency is not CNY")
		return
	}
	if o.Status != payment.StatusPaid {
		writeJSON(w, http.StatusOK, map[string]string{"code": "SUCCESS"}) // nothing to do
		return
	}
	err := s.completeOrder(r.Context(), &payment.Notification{OrderID: o.OutTradeNo, TradeNo: o.TradeNo, AmountCents: o.Amount, Paid: true}, gatewayMethod)
	switch {
	case errors.Is(err, store.ErrNotFound):
		gatewayFail(w, http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
	case err != nil:
		slog.Error("gateway notify failed", "order", o.OutTradeNo, "err", err)
		gatewayFail(w, http.StatusBadRequest, "REJECTED", err.Error()) // e.g. amount mismatch; the center retries
	default:
		writeJSON(w, http.StatusOK, map[string]string{"code": "SUCCESS"})
	}
}
