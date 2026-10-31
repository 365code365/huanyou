// Package paycenter is a simulated external payment center that implements
// the "下单链接" protocol from the payment center's side (see
// internal/api/payment_doc.md). It is used by the end-to-end tests and by
// cmd/paycenter-mock for trying the integration by hand. It is NOT a real
// payment system: "paying" is a button.
//
//	GET  /checkout?<signed params>   verify the link signature, show the order
//	POST /checkout/pay               mark it paid, then notify the merchant
//	POST /api/order/query            sealed query (merchant -> center)
//	GET  /api/orders                 debug: orders and notify attempts (JSON)
package paycenter

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"sync"
	"time"

	"vpnplatform/server/internal/payment"
)

// Order is the center's view of one merchant order.
type Order struct {
	payment.GatewayOrder
	NotifyURL      string   `json:"notify_url"`
	ReturnURL      string   `json:"return_url"`
	NotifyAttempts int      `json:"notify_attempts"`
	NotifyResults  []string `json:"notify_results"`
	Notified       bool     `json:"notified"` // merchant answered SUCCESS
}

// Center is a simulated payment center.
type Center struct {
	gw *payment.Gateway // same keys as the merchant (protocol is symmetric)
	// DropNotify simulates lost notifications: orders get paid but the
	// merchant is never told, so only its active query can find out.
	DropNotify bool
	// Retries and Backoff of the notify sender (defaults 3, 200 ms).
	Retries int
	Backoff time.Duration
	HTTP    *http.Client

	mu     sync.Mutex
	orders map[string]*Order
}

// New creates a center sharing the merchant's id and keys.
func New(merchantID, signKeyHex, encKeyHex string) (*Center, error) {
	gw, err := payment.NewGateway("", "", merchantID, signKeyHex, encKeyHex)
	if err != nil {
		return nil, err
	}
	return &Center{gw: gw, Retries: 3, Backoff: 200 * time.Millisecond,
		HTTP: &http.Client{Timeout: 10 * time.Second}, orders: map[string]*Order{}}, nil
}

// Handler serves the center's pages and API.
func (c *Center) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /checkout", c.checkout)
	mux.HandleFunc("POST /checkout/pay", c.pay)
	mux.HandleFunc("POST /api/order/query", c.query)
	mux.HandleFunc("GET /api/orders", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(c.orders)
	})
	return mux
}

// Order returns a copy of an order (tests).
func (c *Center) Order(id string) (Order, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	o, ok := c.orders[id]
	if !ok {
		return Order{}, false
	}
	return *o, true
}

var page = template.Must(template.New("p").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>模拟支付中心</title>
<style>body{font-family:-apple-system,sans-serif;max-width:420px;margin:12vh auto;padding:0 24px;color:#222}
.card{border:1px solid #e5e5e5;border-radius:14px;padding:22px}.amt{font-size:34px;font-weight:700;margin:8px 0 18px}
button{width:100%;font-size:17px;padding:14px;border:0;border-radius:10px;background:#1677ff;color:#fff}
.muted{color:#888;font-size:13px}.ok{color:#12a150}.err{color:#d64545}</style></head><body>
<div class="card"><div class="muted">模拟支付中心 · 测试环境，不会真实扣款</div>
{{if .Err}}<h3 class="err">{{.Err}}</h3>{{else}}
<h3>{{.Subject}}</h3><div class="muted">订单号 {{.ID}}</div><div class="amt">¥{{.Yuan}}</div>
{{if .Paid}}<p class="ok">已支付（{{.Note}}）</p>{{if .Return}}<a href="{{.Return}}">返回商户</a>{{end}}
{{else}}<form method="post" action="/checkout/pay"><input type="hidden" name="out_trade_no" value="{{.ID}}">
<button type="submit">确认支付 ¥{{.Yuan}}</button></form>{{end}}{{end}}</div></body></html>`))

type pageData struct {
	Err, ID, Subject, Yuan, Note, Return string
	Paid                                 bool
}

// checkout verifies the signed link (as a real center must) and records the order.
func (c *Center) checkout(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if q.Get("merchant_id") != c.gw.MerchantID() {
		w.WriteHeader(http.StatusForbidden)
		_ = page.Execute(w, pageData{Err: "商户号不匹配"})
		return
	}
	if err := c.gw.VerifyLink(q, time.Now()); err != nil {
		w.WriteHeader(http.StatusForbidden)
		_ = page.Execute(w, pageData{Err: "下单链接校验失败：" + err.Error()})
		return
	}
	amount, err := strconv.ParseInt(q.Get("amount"), 10, 64)
	if err != nil || amount <= 0 || q.Get("currency") != "CNY" {
		w.WriteHeader(http.StatusBadRequest)
		_ = page.Execute(w, pageData{Err: "金额或币种无效"})
		return
	}
	id := q.Get("out_trade_no")
	c.mu.Lock()
	o, ok := c.orders[id]
	if !ok {
		o = &Order{GatewayOrder: payment.GatewayOrder{OutTradeNo: id, Amount: amount, Currency: "CNY",
			Subject: q.Get("subject"), Status: "PENDING", CreatedAt: time.Now().Unix()},
			NotifyURL: q.Get("notify_url"), ReturnURL: q.Get("return_url")}
		c.orders[id] = o
	} else if o.Amount != amount {
		c.mu.Unlock()
		w.WriteHeader(http.StatusConflict)
		_ = page.Execute(w, pageData{Err: "同一订单号的金额与之前不一致"})
		return
	}
	d := pageData{ID: id, Subject: o.Subject, Yuan: payment.Yuan(o.Amount), Paid: o.Status == payment.StatusPaid, Return: o.ReturnURL}
	c.mu.Unlock()
	_ = page.Execute(w, d)
}

// pay marks an order paid and notifies the merchant (unless DropNotify).
func (c *Center) pay(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("out_trade_no")
	o, err := c.Pay(id)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_ = page.Execute(w, pageData{Err: err.Error()})
		return
	}
	note := "已通知商户"
	if c.DropNotify {
		note = "模拟通知丢失，等待商户主动查询"
	}
	_ = page.Execute(w, pageData{ID: id, Subject: o.Subject, Yuan: payment.Yuan(o.Amount), Paid: true, Note: note, Return: o.ReturnURL})
}

// Pay marks the order paid (as if the user paid) and, unless DropNotify,
// sends the notification in the background with retries.
func (c *Center) Pay(id string) (Order, error) {
	c.mu.Lock()
	o, ok := c.orders[id]
	if !ok {
		c.mu.Unlock()
		return Order{}, errors.New("订单不存在（请先打开下单链接）")
	}
	if o.Status != payment.StatusPaid {
		o.Status = payment.StatusPaid
		o.PaidAt = time.Now().Unix()
		o.TradeNo = fmt.Sprintf("PC%s%04d", time.Now().Format("20060102150405"), rand.IntN(10000))
	}
	cp := *o
	c.mu.Unlock()
	if !c.DropNotify {
		go c.notify(id)
	}
	return cp, nil
}

// Notify sends the notification now (blocking, with retries); tests use it
// to deliver a notification that was dropped before.
func (c *Center) Notify(id string) error { return c.notify(id) }

func (c *Center) notify(id string) error {
	var last error
	for i := 0; i <= c.Retries; i++ {
		if i > 0 {
			time.Sleep(c.Backoff * time.Duration(i))
		}
		c.mu.Lock()
		o, ok := c.orders[id]
		if !ok {
			c.mu.Unlock()
			return errors.New("no order")
		}
		msg, url := o.GatewayOrder, o.NotifyURL
		o.NotifyAttempts++
		c.mu.Unlock()
		res, err := c.postNotify(url, msg)
		c.mu.Lock()
		o.NotifyResults = append(o.NotifyResults, res)
		if err == nil {
			o.Notified = true
		}
		c.mu.Unlock()
		if err == nil {
			return nil
		}
		last = err
		slog.Warn("paycenter notify failed", "order", id, "attempt", i+1, "err", err)
	}
	return last
}

func (c *Center) postNotify(url string, o payment.GatewayOrder) (string, error) {
	env, err := c.gw.Seal(o) // fresh timestamp + nonce for every attempt
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(env)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "network: " + err.Error(), err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	res := fmt.Sprintf("HTTP %d %s", resp.StatusCode, bytes.TrimSpace(body))
	var ack struct{ Code string }
	_ = json.Unmarshal(body, &ack)
	if resp.StatusCode != http.StatusOK || ack.Code != "SUCCESS" {
		return res, errors.New(res)
	}
	return res, nil
}

// query answers the merchant's sealed order query.
func (c *Center) query(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	var env payment.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "body must be a JSON envelope")
		return
	}
	var q payment.GatewayOrder
	if err := c.gw.Open(&env, &q); err != nil {
		code := "BAD_ENVELOPE"
		if errors.Is(err, payment.ErrBadSignature) {
			code = "BAD_SIGN"
		}
		fail(w, http.StatusUnauthorized, code, err.Error())
		return
	}
	c.mu.Lock()
	o, ok := c.orders[q.OutTradeNo]
	var out payment.GatewayOrder
	if ok {
		out = o.GatewayOrder
	}
	c.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
		return
	}
	resp, err := c.gw.Seal(out)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": msg})
}

// RandomHex is a helper for keys in tests / the demo binary.
func RandomHex(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return hex.EncodeToString(b)
}
