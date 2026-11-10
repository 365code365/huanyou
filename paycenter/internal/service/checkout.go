package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"paycenter/internal/store"
	"paycenter/internal/xinlian"
)

// maxAmount guards against absurd amounts in a (validly signed) link.
const maxAmount = 10_000_000 // cents, 10 万元

// reuseAttempt: clicking the same method again within this window reuses
// the upstream order instead of creating a new one.
const reuseAttempt = 10 * time.Minute

// Checkout verifies a checkout link from the VPN app and records the order.
// The same out_trade_no always maps to the same order; a different amount
// for it is refused.
func (s *Service) Checkout(q url.Values, ip string) (*store.Order, error) {
	now := time.Now()
	m, err := s.merchant(q.Get("merchant_id"))
	if err != nil || !m.Enabled {
		return nil, userErr(http.StatusForbidden, "商户不存在或已停用")
	}
	codec, err := Codec(m)
	if err != nil {
		slog.Error("merchant keys invalid", "merchant", m.MerchantID, "err", err)
		return nil, userErr(http.StatusServiceUnavailable, "商户密钥配置有误，请联系客服")
	}
	if err := codec.VerifyLink(q, now); err != nil {
		slog.Warn("checkout link rejected", "merchant", m.MerchantID, "ip", ip, "err", err)
		return nil, userErr(http.StatusForbidden, "下单链接校验失败：%s", err.Error())
	}
	amount, err := strconv.ParseInt(q.Get("amount"), 10, 64)
	if err != nil || amount <= 0 || amount > maxAmount {
		return nil, userErr(http.StatusBadRequest, "金额无效")
	}
	if q.Get("currency") != "CNY" {
		return nil, userErr(http.StatusBadRequest, "只支持人民币")
	}
	out := strings.TrimSpace(q.Get("out_trade_no"))
	if out == "" || len(out) > 64 {
		return nil, userErr(http.StatusBadRequest, "订单号无效")
	}
	expireAt, _ := strconv.ParseInt(q.Get("expire_at"), 10, 64)
	// A link made by 测试下单 (the signature above proves it used this
	// merchant's keys): it is a test order, never notified to the real VPN.
	var run store.TestRun
	isTest := s.DB.Where("merchant_id = ? AND out_trade_no = ?", m.MerchantID, out).Take(&run).Error == nil
	notifyURL := m.NotifyURL
	if notifyURL == "" || isTest {
		notifyURL = q.Get("notify_url")
	}
	if !httpURL(notifyURL) {
		return nil, userErr(http.StatusBadRequest, "通知地址无效")
	}
	returnURL := q.Get("return_url")
	if returnURL != "" && !httpURL(returnURL) {
		returnURL = ""
	}
	subject := store.Trunc(strings.TrimSpace(q.Get("subject")), 120)

	var o store.Order
	err = s.DB.Where("merchant_id = ? AND out_trade_no = ?", m.MerchantID, out).Take(&o).Error
	if errNotFound(err) {
		o = store.Order{TradeNo: newNo("PC", 5), MerchantID: m.MerchantID, OutTradeNo: out, Amount: amount,
			Currency: "CNY", Subject: subject, NotifyURL: notifyURL, ReturnURL: returnURL, ExpireAt: expireAt,
			Status: store.OrderPending, ChannelID: m.ChannelID, ClientIP: ip, IsTest: isTest}
		if err = s.DB.Create(&o).Error; err == nil {
			store.Log(s.DB, o.ID, "checkout", fmt.Sprintf("收到下单链接 金额 %s 元 IP %s", xinlian.CentsToYuan(amount), ip))
			return &o, nil
		}
		// Lost a race with a concurrent open of the same link.
		err = s.DB.Where("merchant_id = ? AND out_trade_no = ?", m.MerchantID, out).Take(&o).Error
	}
	if err != nil {
		return nil, err
	}
	if o.Amount != amount {
		return nil, userErr(http.StatusConflict, "同一订单号的金额与之前不一致")
	}
	if o.Status == store.OrderPaid {
		return &o, nil
	}
	// A fresh link for the same order: extend the expiry, refresh the URLs.
	upd := map[string]any{"notify_url": notifyURL, "return_url": returnURL, "subject": subject}
	if expireAt > o.ExpireAt {
		upd["expire_at"] = expireAt
		o.ExpireAt = expireAt
	}
	if err := s.DB.Model(&o).Updates(upd).Error; err != nil {
		return nil, err
	}
	o.NotifyURL, o.ReturnURL, o.Subject = notifyURL, returnURL, subject
	return &o, nil
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// OrderByTradeNo loads an order by our trade number.
func (s *Service) OrderByTradeNo(no string) (*store.Order, error) {
	var o store.Order
	if err := s.DB.Where("trade_no = ?", no).Take(&o).Error; err != nil {
		return nil, err
	}
	return &o, nil
}

// PayTypes lists the methods offered for an order (its merchant's channel),
// without the hidden ones.
func (s *Service) PayTypes(o *store.Order) ([]store.PayType, error) {
	ch, err := s.orderChannel(o)
	if err != nil {
		return nil, err
	}
	types := ch.VisiblePayTypes()
	if len(types) == 0 {
		return nil, userErr(http.StatusServiceUnavailable, "支付方式维护中，请稍后再试或联系客服")
	}
	return types, nil
}

func (s *Service) orderChannel(o *store.Order) (*store.Channel, error) {
	m, err := s.merchant(o.MerchantID)
	if err != nil {
		return nil, userErr(http.StatusServiceUnavailable, "商户不存在")
	}
	id := m.ChannelID
	if id == 0 {
		id = o.ChannelID
	}
	ch, err := s.channel(id)
	if err != nil || !ch.Enabled {
		return nil, userErr(http.StatusServiceUnavailable, "暂无可用的支付通道，请联系客服")
	}
	return ch, nil
}

// StartPay creates (or reuses) the upstream order for a method and returns
// what the cashier should do next.
func (s *Service) StartPay(ctx context.Context, o *store.Order, payType, device string) (*store.Attempt, xinlian.Action, error) {
	none := xinlian.Action{}
	o, err := s.OrderByTradeNo(o.TradeNo) // fresh status
	if err != nil {
		return nil, none, err
	}
	now := time.Now()
	if o.Status == store.OrderPaid {
		return nil, none, userErr(http.StatusConflict, "订单已支付")
	}
	if o.Expired(now) {
		return nil, none, userErr(http.StatusGone, "订单已过期，请返回 App 重新下单")
	}
	ch, err := s.orderChannel(o)
	if err != nil {
		return nil, none, err
	}
	all := ch.PayTypeList()
	i := slices.IndexFunc(all, func(p store.PayType) bool { return p.Code == payType })
	if i < 0 {
		return nil, none, userErr(http.StatusBadRequest, "不支持的支付方式")
	}
	if all[i].Hidden {
		return nil, none, userErr(http.StatusServiceUnavailable, "该支付方式维护中，请换一种支付方式")
	}
	if ch.Device != "" && ch.Device != "auto" {
		device = ch.Device
	}
	var prev store.Attempt
	err = s.DB.Where("order_id = ? AND pay_type = ? AND device = ? AND channel_id = ? AND status = ? AND created_at > ?",
		o.ID, payType, device, ch.ID, store.AttemptPending, now.Add(-reuseAttempt)).
		Order("id DESC").Take(&prev).Error
	if err == nil && prev.Payload != "" {
		return &prev, xinlian.ParsePayload(prev.RespType, json.RawMessage(prev.Payload)), nil
	}
	pub := s.PublicURL()
	if pub == "" {
		return nil, none, userErr(http.StatusServiceUnavailable, "支付中心尚未配置对外地址，请联系客服")
	}
	cl, err := s.Client(ch)
	if err != nil {
		slog.Error("channel config invalid", "channel", ch.ID, "err", err)
		return nil, none, userErr(http.StatusServiceUnavailable, "支付通道配置有误，请联系客服")
	}
	a := store.Attempt{OrderID: o.ID, UpstreamOutTradeNo: newNo("PA", 5), ChannelID: ch.ID, PayType: payType,
		Device: device, Money: xinlian.CentsToYuan(o.Amount), Status: store.AttemptPending}
	if err := s.DB.Create(&a).Error; err != nil {
		return nil, none, err
	}
	name := o.Subject
	if name == "" {
		name = "订单 " + o.OutTradeNo
	}
	cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	res, err := cl.Submit(cctx, xinlian.SubmitRequest{Type: payType, OutTradeNo: a.UpstreamOutTradeNo, Money: a.Money,
		Name: name, NotifyURL: fmt.Sprintf("%s/notify/xinlian/%d", pub, ch.ID),
		ReturnURL: pub + "/pay/" + o.TradeNo + "/return?t=" + s.PageToken(o.TradeNo), Device: device})
	if err == nil && res.Money != "" {
		if c, perr := xinlian.YuanToCents(res.Money); perr != nil || c != o.Amount {
			err = fmt.Errorf("上游返回金额 %s 与订单金额 %s 不一致", res.Money, a.Money)
		}
	}
	if err != nil {
		s.DB.Model(&a).Updates(map[string]any{"status": store.AttemptFailed, "error": store.Trunc(err.Error(), 500)})
		store.Log(s.DB, o.ID, "submit_fail", fmt.Sprintf("%s 下单失败：%v", payType, err))
		slog.Warn("upstream submit failed", "order", o.TradeNo, "type", payType, "err", err)
		return nil, none, userErr(http.StatusBadGateway, "发起支付失败，请稍后再试或换一种支付方式")
	}
	act := xinlian.ParsePayload(res.Type, res.Payload)
	a.UpstreamTradeNo, a.RespType, a.Payload = res.PlatformTradeNo(), res.Type, string(res.Payload)
	s.DB.Model(&a).Updates(map[string]any{"upstream_trade_no": a.UpstreamTradeNo, "resp_type": a.RespType, "payload": a.Payload})
	s.DB.Model(o).Updates(map[string]any{"pay_type": payType, "channel_id": ch.ID})
	store.Log(s.DB, o.ID, "submit", fmt.Sprintf("%s 下单成功 上游单号 %s 受理类型 %s", payType, a.UpstreamTradeNo, res.Type))
	if act.Kind == "none" {
		slog.Warn("upstream payload not understood", "order", o.TradeNo, "type", res.Type, "payload", string(res.Payload))
		return &a, act, userErr(http.StatusBadGateway, "暂不支持该支付方式返回的参数，请换一种支付方式")
	}
	return &a, act, nil
}
