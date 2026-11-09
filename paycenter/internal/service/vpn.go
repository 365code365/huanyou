package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"paycenter/internal/store"
	"paycenter/internal/vpnproto"
)

// QueryReply is the HTTP reply to a VPN order query.
type QueryReply struct {
	Status int
	Body   any // *vpnproto.Envelope or {"code","message"}
}

func fail(status int, code, msg string) QueryReply {
	return QueryReply{Status: status, Body: map[string]string{"code": code, "message": msg}}
}

// HandleVPNQuery answers POST /api/order/query (sealed request and response).
func (s *Service) HandleVPNQuery(ctx context.Context, body []byte, ip string) QueryReply {
	var env vpnproto.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fail(http.StatusBadRequest, "BAD_REQUEST", "body must be a JSON envelope")
	}
	m, err := s.merchant(env.MerchantID)
	if err != nil || !m.Enabled {
		slog.Warn("query from unknown merchant", "merchant", env.MerchantID, "ip", ip)
		return fail(http.StatusUnauthorized, "BAD_ENVELOPE", "unknown merchant")
	}
	codec, err := Codec(m)
	if err != nil {
		return fail(http.StatusServiceUnavailable, "NOT_CONFIGURED", "merchant keys invalid")
	}
	var q vpnproto.Order
	if err := codec.Open(&env, &q, &s.Nonces, time.Now()); err != nil {
		slog.Warn("query envelope rejected", "merchant", m.MerchantID, "ip", ip, "err", err)
		if errors.Is(err, vpnproto.ErrBadSignature) {
			return fail(http.StatusUnauthorized, "BAD_SIGN", err.Error())
		}
		return fail(http.StatusUnauthorized, "BAD_ENVELOPE", err.Error())
	}
	var o store.Order
	if err := s.DB.Where("merchant_id = ? AND out_trade_no = ?", m.MerchantID, q.OutTradeNo).Take(&o).Error; err != nil {
		if errNotFound(err) {
			return fail(http.StatusNotFound, "ORDER_NOT_FOUND", "order not found")
		}
		return fail(http.StatusInternalServerError, "INTERNAL", "db error")
	}
	if o.Status != store.OrderPaid {
		// The VPN waits at most 6 s; stay well inside that.
		sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		s.SyncIfStale(sctx, &o, 5*time.Second)
		cancel()
	}
	resp, err := codec.Seal(vpnOrder(&o), time.Now())
	if err != nil {
		return fail(http.StatusInternalServerError, "INTERNAL", err.Error())
	}
	return QueryReply{Status: http.StatusOK, Body: resp}
}

func vpnOrder(o *store.Order) vpnproto.Order {
	v := vpnproto.Order{OutTradeNo: o.OutTradeNo, TradeNo: o.TradeNo, Amount: o.Amount, Currency: o.Currency,
		Subject: o.Subject, Status: o.VPNStatus(time.Now()), CreatedAt: o.CreatedAt.Unix()}
	if o.PaidAt != nil {
		v.PaidAt = o.PaidAt.Unix()
	}
	return v
}

// notifyDelays: retry schedule after a failed notification (VPN doc: 1 min,
// 5 min, 30 min, 2 h ... up to 24 h after payment).
var notifyDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 4 * time.Hour, 8 * time.Hour}

const notifyWindow = 24 * time.Hour

// NotifyResult is one notification attempt.
type NotifyResult struct {
	OK     bool
	Detail string
}

// SendNotify posts the sealed paid notification to the merchant now.
func (s *Service) SendNotify(ctx context.Context, o *store.Order) NotifyResult {
	m, err := s.merchant(o.MerchantID)
	if err != nil {
		return NotifyResult{Detail: "商户不存在"}
	}
	if o.IsTest {
		return s.postNotify(ctx, m, "", vpnOrder(o)) // the built-in "模拟 VPN"
	}
	target := o.NotifyURL
	if m.NotifyURL != "" {
		target = m.NotifyURL
	}
	return s.postNotify(ctx, m, target, vpnOrder(o))
}

func (s *Service) postNotify(ctx context.Context, m *store.Merchant, target string, msg vpnproto.Order) NotifyResult {
	codec, err := Codec(m)
	if err != nil {
		return NotifyResult{Detail: "商户密钥无效：" + err.Error()}
	}
	env, err := codec.Seal(msg, time.Now()) // fresh timestamp + nonce every attempt
	if err != nil {
		return NotifyResult{Detail: err.Error()}
	}
	b, _ := json.Marshal(env)
	if target == "" {
		// Delivered in-process: same sealed bytes, same checks as over HTTP,
		// without depending on this host resolving its own public URL.
		st, rep := s.ReceiveTestNotify(b)
		rb, _ := json.Marshal(rep)
		return NotifyResult{OK: st == http.StatusOK && rep["code"] == "SUCCESS", Detail: fmt.Sprintf("模拟 VPN HTTP %d %s", st, rb)}
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return NotifyResult{Detail: "通知地址无效：" + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return NotifyResult{Detail: "网络错误：" + err.Error()}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	var ack struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &ack)
	detail := fmt.Sprintf("HTTP %d %s", res.StatusCode, bytes.TrimSpace(body))
	return NotifyResult{OK: res.StatusCode == http.StatusOK && ack.Code == "SUCCESS", Detail: store.Trunc(detail, 480)}
}

// TestNotify sends a PAID notification for a made-up order. A VPN with the
// same keys answers 404 ORDER_NOT_FOUND (keys fine, no such order); wrong
// keys give 401 BAD_SIGN.
func (s *Service) TestNotify(ctx context.Context, m *store.Merchant, target string) NotifyResult {
	no := newNo("PCTEST", 3)
	return s.postNotify(ctx, m, target, vpnproto.Order{OutTradeNo: no, TradeNo: no, Amount: 1, Currency: "CNY",
		Status: vpnproto.StatusPaid, PaidAt: time.Now().Unix()})
}

// notifyDue sends every due notification.
func (s *Service) notifyDue(ctx context.Context) {
	for ctx.Err() == nil {
		var due []store.Order
		if err := s.DB.Where("notify_status = ? AND next_notify_at <= ?", store.NotifyPending, time.Now()).
			Order("next_notify_at").Limit(50).Find(&due).Error; err != nil {
			slog.Error("load due notifications", "err", err)
			return
		}
		if len(due) == 0 {
			return
		}
		for i := range due {
			s.notifyOne(ctx, &due[i])
		}
		if len(due) < 50 {
			return
		}
	}
}

func (s *Service) notifyOne(ctx context.Context, o *store.Order) {
	r := s.SendNotify(ctx, o)
	now := time.Now()
	n := o.NotifyAttempts + 1
	upd := map[string]any{"notify_attempts": n, "last_notify_at": now, "last_notify_result": r.Detail}
	switch {
	case r.OK:
		upd["notify_status"], upd["next_notify_at"] = store.NotifySuccess, nil
		store.Log(s.DB, o.ID, "notify_ok", fmt.Sprintf("第 %d 次通知成功", n))
	default:
		d := notifyDelays[min(n-1, len(notifyDelays)-1)]
		next := now.Add(d)
		paid := now
		if o.PaidAt != nil {
			paid = *o.PaidAt
		}
		if next.Sub(paid) > notifyWindow {
			upd["notify_status"], upd["next_notify_at"] = store.NotifyFailed, nil
			store.Log(s.DB, o.ID, "notify_giveup", fmt.Sprintf("第 %d 次通知失败（%s），超过 24 小时，停止重试", n, r.Detail))
		} else {
			upd["next_notify_at"] = next
			store.Log(s.DB, o.ID, "notify_fail", fmt.Sprintf("第 %d 次通知失败（%s），%s 后重试", n, r.Detail, d))
		}
		slog.Warn("vpn notify failed", "order", o.TradeNo, "attempt", n, "result", r.Detail)
	}
	s.DB.Model(o).Updates(upd)
}

// Renotify queues an immediate notification (admin action).
func (s *Service) Renotify(o *store.Order) error {
	if o.Status != store.OrderPaid {
		return errors.New("订单未支付")
	}
	err := s.DB.Model(o).Updates(map[string]any{"notify_status": store.NotifyPending, "next_notify_at": time.Now()}).Error
	if err == nil {
		store.Log(s.DB, o.ID, "renotify", "管理员手动重发通知")
		s.Kick()
	}
	return err
}

// Run starts the background workers until ctx ends.
func (s *Service) Run(ctx context.Context) {
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			s.notifyDue(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-s.kick:
			}
		}
	}()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.pollPending(ctx)
			}
		}
	}()
}

// pollPending is the fallback for lost upstream callbacks: pending upstream
// orders are queried every minute for 2 hours, then every 15 minutes up to 24 h.
func (s *Service) pollPending(ctx context.Context) {
	now := time.Now()
	var list []store.Attempt
	err := s.DB.Where("status = ? AND created_at > ?", store.AttemptPending, now.Add(-24*time.Hour)).
		Where(s.DB.Where("created_at > ? AND (last_query_at IS NULL OR last_query_at < ?)", now.Add(-2*time.Hour), now.Add(-time.Minute)).
			Or("created_at <= ? AND (last_query_at IS NULL OR last_query_at < ?)", now.Add(-2*time.Hour), now.Add(-15*time.Minute))).
		Order("id").Limit(100).Find(&list).Error
	if err != nil {
		slog.Error("load pending attempts", "err", err)
		return
	}
	for i := range list {
		if ctx.Err() != nil {
			return
		}
		var o store.Order
		if s.DB.Take(&o, list[i].OrderID).Error != nil {
			continue
		}
		if err := s.syncAttempt(ctx, &list[i], &o); err != nil {
			slog.Debug("poll attempt", "attempt", list[i].UpstreamOutTradeNo, "err", err)
		}
	}
}
