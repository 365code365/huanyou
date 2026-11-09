package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"paycenter/internal/store"
	"paycenter/internal/xinlian"
)

// HandleUpstreamNotify processes a 信联pay callback. A nil error means the
// platform should get "success".
func (s *Service) HandleUpstreamNotify(channelID uint, form url.Values) error {
	ch, err := s.channel(channelID)
	if err != nil {
		return errors.New("unknown channel")
	}
	cl, err := s.Client(ch)
	if err != nil {
		return err
	}
	if err := cl.VerifyNotify(form); err != nil {
		return err
	}
	if form.Get("trade_status") != xinlian.TradeSuccess {
		return nil // nothing to do; acknowledge so it stops retrying
	}
	var a store.Attempt
	if err := s.DB.Where("upstream_out_trade_no = ?", form.Get("out_trade_no")).Take(&a).Error; err != nil {
		return fmt.Errorf("unknown out_trade_no %q", form.Get("out_trade_no"))
	}
	if a.ChannelID != ch.ID {
		return errors.New("attempt belongs to another channel")
	}
	if err := sameMoney(form.Get("money"), a.Money); err != nil {
		store.Log(s.DB, a.OrderID, "notify_reject", "上游回调金额不符："+err.Error())
		return err
	}
	return s.markPaid(&a, form.Get("trade_no"), "异步回调")
}

func sameMoney(got, want string) error {
	g, err := xinlian.YuanToCents(got)
	if err != nil {
		return fmt.Errorf("金额格式错误 %q", got)
	}
	w, _ := xinlian.YuanToCents(want)
	if g != w {
		return fmt.Errorf("上游金额 %s，订单金额 %s", got, want)
	}
	return nil
}

// markPaid records a confirmed payment once and queues the VPN notification.
func (s *Service) markPaid(a *store.Attempt, upstreamTradeNo, source string) error {
	now := time.Now()
	if upstreamTradeNo == "" {
		upstreamTradeNo = a.UpstreamTradeNo
	}
	ares := s.DB.Model(&store.Attempt{}).Where("id = ? AND status <> ?", a.ID, store.AttemptPaid).
		Updates(map[string]any{"status": store.AttemptPaid, "paid_at": now, "upstream_trade_no": upstreamTradeNo})
	if ares.Error != nil {
		return ares.Error
	}
	res := s.DB.Model(&store.Order{}).Where("id = ? AND status <> ?", a.OrderID, store.OrderPaid).Updates(map[string]any{
		"status": store.OrderPaid, "paid_at": now, "upstream_trade_no": upstreamTradeNo, "pay_type": a.PayType,
		"channel_id": a.ChannelID, "notify_status": store.NotifyPending, "next_notify_at": now,
	})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 {
		store.Log(s.DB, a.OrderID, "paid", fmt.Sprintf("%s确认已支付 %s 元（%s，上游单号 %s）", source, a.Money, a.PayType, upstreamTradeNo))
		slog.Info("order paid", "order_id", a.OrderID, "attempt", a.UpstreamOutTradeNo, "source", source)
		s.Kick()
		return nil
	}
	if ares.RowsAffected == 1 {
		// This upstream order is newly paid but the order already was, through
		// another method: the money came in twice and needs a manual refund.
		store.Log(s.DB, a.OrderID, "duplicate", fmt.Sprintf("重复支付：上游单号 %s 也已支付 %s 元，需人工退款", upstreamTradeNo, a.Money))
		slog.Warn("duplicate payment", "order_id", a.OrderID, "attempt", a.UpstreamOutTradeNo)
	}
	return nil
}

// SyncOrder asks 信联pay about the order's pending upstream orders.
func (s *Service) SyncOrder(ctx context.Context, o *store.Order) error {
	if _, busy := s.syncing.LoadOrStore(o.ID, struct{}{}); busy {
		return nil
	}
	defer s.syncing.Delete(o.ID)
	var list []store.Attempt
	if err := s.DB.Where("order_id = ? AND status = ?", o.ID, store.AttemptPending).Order("id DESC").Limit(5).Find(&list).Error; err != nil {
		return err
	}
	var last error
	for i := range list {
		if err := s.syncAttempt(ctx, &list[i], o); err != nil {
			last = err
		}
	}
	now := time.Now()
	s.DB.Model(o).Update("last_sync_at", now)
	o.LastSyncAt = &now
	return last
}

// SyncIfStale is SyncOrder at most once per interval.
func (s *Service) SyncIfStale(ctx context.Context, o *store.Order, every time.Duration) {
	if o.Status == store.OrderPaid || (o.LastSyncAt != nil && time.Since(*o.LastSyncAt) < every) {
		return
	}
	if err := s.SyncOrder(ctx, o); err != nil {
		slog.Debug("sync order", "order", o.TradeNo, "err", err)
	}
	if fresh, err := s.OrderByTradeNo(o.TradeNo); err == nil {
		*o = *fresh
	}
}

// closeGrace: after the order expires, pending upstream orders are closed so
// they can't be paid any more.
const closeGrace = 5 * time.Minute

func (s *Service) syncAttempt(ctx context.Context, a *store.Attempt, o *store.Order) error {
	ch, err := s.channel(a.ChannelID)
	if err != nil {
		return err
	}
	cl, err := s.Client(ch)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := time.Now()
	s.DB.Model(a).Update("last_query_at", now)
	r, err := cl.Query(cctx, a.UpstreamOutTradeNo)
	if xinlian.IsCode(err, xinlian.CodeNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch r.TradeStatus {
	case xinlian.TradeSuccess:
		if err := sameMoney(r.Money, a.Money); err != nil {
			store.Log(s.DB, a.OrderID, "query_reject", "查询结果金额不符："+err.Error())
			return err
		}
		return s.markPaid(a, r.TradeNo, "主动查询")
	case xinlian.TradeClosed:
		s.DB.Model(a).Update("status", store.AttemptClosed)
	case xinlian.TradeWait:
		if o != nil && o.Status != store.OrderPaid && now.Unix() > o.ExpireAt+int64(closeGrace/time.Second) {
			if err := cl.Close(cctx, a.UpstreamOutTradeNo); err == nil {
				s.DB.Model(a).Update("status", store.AttemptClosed)
				store.Log(s.DB, a.OrderID, "close", "订单过期，已关闭上游订单 "+a.UpstreamOutTradeNo)
			}
		}
	}
	return nil
}
