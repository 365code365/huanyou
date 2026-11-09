package service

// 测试下单: the pay center plays the VPN side so the whole integration can be
// checked without the VPN system. It signs checkout links with a merchant's
// keys exactly like the VPN, receives the paid notification on a built-in
// "模拟 VPN" endpoint (same envelope checks as the VPN), and sends sealed
// order queries the way the VPN does.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"paycenter/internal/store"
	"paycenter/internal/vpnproto"
	"paycenter/internal/xinlian"
)

// TestNotifyPath is the built-in "模拟 VPN" notify endpoint.
const TestNotifyPath = "/test/vpn-notify"

// Step is one check of the self-test.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Skip   bool   `json:"skip,omitempty"`
	Detail string `json:"detail"`
	Ms     int64  `json:"ms"`
}

func (s *Service) merchantCodec(id string) (*store.Merchant, *vpnproto.Codec, error) {
	m, err := s.merchant(id)
	if err != nil {
		return nil, nil, userErr(http.StatusBadRequest, "商户不存在")
	}
	c, err := Codec(m)
	if err != nil {
		return nil, nil, userErr(http.StatusBadRequest, "商户密钥无效：%v", err)
	}
	return m, c, nil
}

// CreateTestRun makes a checkout link the way the VPN does (same parameters,
// same HMAC-SHA256 signature), pointing notifications at the built-in receiver.
func (s *Service) CreateTestRun(merchantID string, cents int64, subject, kind string) (*store.TestRun, error) {
	pub := s.PublicURL()
	if pub == "" {
		return nil, userErr(http.StatusBadRequest, "请先在「系统设置」填写对外地址")
	}
	_, codec, err := s.merchantCodec(merchantID)
	if err != nil {
		return nil, err
	}
	if cents <= 0 || cents > maxAmount {
		return nil, userErr(http.StatusBadRequest, "金额需在 0.01 ～ 100000 元之间")
	}
	subject = store.Trunc(strings.TrimSpace(subject), 60)
	if subject == "" {
		subject = "测试订单"
	}
	out := newNo("VPTEST", 3)
	now := time.Now()
	nb := make([]byte, 16)
	_, _ = rand.Read(nb)
	p := map[string]string{
		"merchant_id": merchantID, "out_trade_no": out, "amount": strconv.FormatInt(cents, 10), "currency": "CNY",
		"subject": subject, "notify_url": pub + TestNotifyPath, "return_url": pub + "/admin/tests?no=" + out,
		"timestamp": strconv.FormatInt(now.Unix(), 10), "expire_at": strconv.FormatInt(now.Add(30*time.Minute).Unix(), 10),
		"nonce": hex.EncodeToString(nb),
	}
	p["sign"] = codec.SignLink(p)
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	run := &store.TestRun{MerchantID: merchantID, OutTradeNo: out, Amount: cents, Subject: subject,
		PayLink: pub + "/checkout?" + q.Encode(), Kind: kind}
	if err := s.DB.Create(run).Error; err != nil {
		return nil, err
	}
	return run, nil
}

// TestOrder is the order of a test run, nil until its link was opened.
func (s *Service) TestOrder(run *store.TestRun) *store.Order {
	var o store.Order
	if s.DB.Where("merchant_id = ? AND out_trade_no = ?", run.MerchantID, run.OutTradeNo).Take(&o).Error != nil {
		return nil
	}
	return &o
}

// ReceiveTestNotify is the "模拟 VPN": it checks a notification like the VPN
// does (envelope, replay, order, amount) and records it.
func (s *Service) ReceiveTestNotify(body []byte) (int, map[string]string) {
	var env vpnproto.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return http.StatusBadRequest, map[string]string{"code": "BAD_REQUEST", "message": "body must be a JSON envelope"}
	}
	m, err := s.merchant(env.MerchantID)
	if err != nil {
		return http.StatusUnauthorized, map[string]string{"code": "BAD_ENVELOPE", "message": "unknown merchant"}
	}
	codec, err := Codec(m)
	if err != nil {
		return http.StatusServiceUnavailable, map[string]string{"code": "NOT_CONFIGURED", "message": err.Error()}
	}
	var o vpnproto.Order
	if err := codec.Open(&env, &o, &s.testNonces, time.Now()); err != nil {
		slog.Warn("test notify rejected", "merchant", env.MerchantID, "err", err)
		code := "BAD_ENVELOPE"
		if errors.Is(err, vpnproto.ErrBadSignature) {
			code = "BAD_SIGN"
		}
		return http.StatusUnauthorized, map[string]string{"code": code, "message": err.Error()}
	}
	var run store.TestRun
	if s.DB.Where("merchant_id = ? AND out_trade_no = ?", m.MerchantID, o.OutTradeNo).Take(&run).Error != nil {
		return http.StatusNotFound, map[string]string{"code": "ORDER_NOT_FOUND"}
	}
	n := store.TestNotice{RunID: run.ID, OutTradeNo: o.OutTradeNo, TradeNo: o.TradeNo, Amount: o.Amount, Status: o.Status, Code: "SUCCESS"}
	status := http.StatusOK
	switch {
	case o.Currency != "" && o.Currency != "CNY":
		n.Code, n.Detail, status = "REJECTED", "币种不是 CNY："+o.Currency, http.StatusBadRequest
	case o.Amount != run.Amount:
		n.Code, n.Detail, status = "REJECTED", fmt.Sprintf("金额 %d 分与订单金额 %d 分不一致", o.Amount, run.Amount), http.StatusBadRequest
	case o.Status != vpnproto.StatusPaid:
		n.Detail = "状态不是 PAID，VPN 不会开通（仍回复 SUCCESS）"
	default:
		n.Detail = "验签、解密、金额核对通过，VPN 会在这里开通套餐"
	}
	_ = s.DB.Create(&n).Error
	if status != http.StatusOK {
		return status, map[string]string{"code": n.Code, "message": n.Detail}
	}
	return status, map[string]string{"code": "SUCCESS"}
}

// SimulatePay marks a test order paid without the upstream (no money moves)
// and triggers the notification.
func (s *Service) SimulatePay(o *store.Order) error {
	if !o.IsTest {
		return userErr(http.StatusBadRequest, "只有测试订单可以模拟支付")
	}
	if o.Status == store.OrderPaid {
		return userErr(http.StatusConflict, "订单已经是已支付")
	}
	a := store.Attempt{OrderID: o.ID, UpstreamOutTradeNo: newNo("PT", 5), PayType: "simulate", Device: "test",
		Money: xinlian.CentsToYuan(o.Amount), Status: store.AttemptPending}
	if err := s.DB.Create(&a).Error; err != nil {
		return err
	}
	return s.markPaid(&a, newNo("SIM", 3), "模拟支付")
}

// QueryTrace shows one VPN-style order query: what was sent and received.
type QueryTrace struct {
	OutTradeNo string
	Plain      string // request plaintext
	Request    string // sealed request envelope
	Status     int
	Response   string // response body
	Decoded    string // decrypted response
	Order      *vpnproto.Order
	Err        string
	Ms         int64
}

// TestQuery sends a sealed query exactly like the VPN would and opens the
// sealed reply with the merchant's keys.
func (s *Service) TestQuery(ctx context.Context, merchantID, out string) QueryTrace {
	t := QueryTrace{OutTradeNo: out}
	_, codec, err := s.merchantCodec(merchantID)
	if err != nil {
		t.Err = err.Error()
		return t
	}
	plain := vpnproto.Order{OutTradeNo: out}
	pb, _ := json.MarshalIndent(plain, "", "  ")
	t.Plain = string(pb)
	env, err := codec.Seal(plain, time.Now())
	if err != nil {
		t.Err = err.Error()
		return t
	}
	b, _ := json.Marshal(env)
	eb, _ := json.MarshalIndent(env, "", "  ")
	t.Request = string(eb)
	start := time.Now()
	rep := s.HandleVPNQuery(ctx, b, "测试")
	t.Ms = time.Since(start).Milliseconds()
	t.Status = rep.Status
	rb, _ := json.MarshalIndent(rep.Body, "", "  ")
	t.Response = string(rb)
	if rep.Status != http.StatusOK {
		code := ""
		if m, ok := rep.Body.(map[string]string); ok {
			code = m["code"]
		}
		t.Err = fmt.Sprintf("HTTP %d %s", rep.Status, code)
		return t
	}
	renv, ok := rep.Body.(*vpnproto.Envelope)
	if !ok {
		t.Err = "响应不是信封"
		return t
	}
	var o vpnproto.Order
	if err := codec.Open(renv, &o, nil, time.Now()); err != nil {
		t.Err = "响应校验失败：" + err.Error()
		return t
	}
	db, _ := json.MarshalIndent(o, "", "  ")
	t.Decoded, t.Order = string(db), &o
	if o.OutTradeNo != out {
		t.Err = "响应的订单号与请求不一致"
	}
	return t
}

func (s *Service) attemptClient(a *store.Attempt) (*xinlian.Client, error) {
	ch, err := s.channel(a.ChannelID)
	if err != nil {
		return nil, errors.New("通道不存在")
	}
	return s.Client(ch)
}

// RunSelfTest runs the whole chain against a fresh test order and stores the
// steps on the run. withUpstream also creates, queries and closes one real
// upstream order (no payment).
func (s *Service) RunSelfTest(ctx context.Context, merchantID string, cents int64, withUpstream bool) (*store.TestRun, error) {
	run, err := s.CreateTestRun(merchantID, cents, "自检订单", "auto")
	if err != nil {
		return nil, err
	}
	var steps []Step
	stop := false
	step := func(name string, critical bool, fn func() (string, error)) {
		if stop {
			steps = append(steps, Step{Name: name, Skip: true, Detail: "前面的关键步骤失败，已跳过"})
			return
		}
		start := time.Now()
		d, err := fn()
		st := Step{Name: name, OK: err == nil, Detail: d, Ms: time.Since(start).Milliseconds()}
		if err != nil {
			st.Detail = err.Error()
			if critical {
				stop = true
			}
		}
		steps = append(steps, st)
	}
	u, _ := url.Parse(run.PayLink)
	q := u.Query()
	var o *store.Order

	step("生成下单链接（按 VPN 规则 HMAC-SHA256 签名）", true, func() (string, error) {
		return fmt.Sprintf("%d 个参数，sign=%s…", len(q), q.Get("sign")[:16]), nil
	})
	step("打开下单链接：验签并建单", true, func() (string, error) {
		var err error
		if o, err = s.Checkout(q, "自检"); err != nil {
			return "", err
		}
		if !o.IsTest {
			return "", errors.New("订单没有被识别为测试订单")
		}
		return "平台单号 " + o.TradeNo, nil
	})
	step("篡改金额的链接应被拒绝", false, func() (string, error) {
		bad := url.Values{}
		for k, v := range q {
			bad[k] = append([]string(nil), v...)
		}
		bad.Set("amount", strconv.FormatInt(cents+100, 10))
		_, err := s.Checkout(bad, "自检")
		var ue *UserError
		switch {
		case err == nil:
			return "", errors.New("篡改后的链接被接受了")
		case errors.As(err, &ue) && ue.Status == http.StatusForbidden:
			return "已拒绝：" + ue.Msg, nil
		}
		return "", fmt.Errorf("拒绝原因不符合预期：%v", err)
	})
	step("重复打开同一链接应返回同一订单", false, func() (string, error) {
		o2, err := s.Checkout(q, "自检")
		if err != nil {
			return "", err
		}
		if o2.TradeNo != o.TradeNo {
			return "", fmt.Errorf("得到了另一笔订单 %s", o2.TradeNo)
		}
		return "同一笔 " + o2.TradeNo, nil
	})
	step("模拟 VPN 查询：应为 PENDING", false, func() (string, error) {
		t := s.TestQuery(ctx, merchantID, run.OutTradeNo)
		if t.Err != "" {
			return "", errors.New(t.Err)
		}
		if t.Order.Status != vpnproto.StatusPending || t.Order.Amount != cents {
			return "", fmt.Errorf("得到 status=%s amount=%d", t.Order.Status, t.Order.Amount)
		}
		return fmt.Sprintf("status=PENDING amount=%d，耗时 %d ms", t.Order.Amount, t.Ms), nil
	})
	if withUpstream {
		var att *store.Attempt
		step("上游下单（信联pay /api/pay/submit）", false, func() (string, error) {
			types, err := s.PayTypes(o)
			if err != nil {
				return "", err
			}
			if len(types) == 0 {
				return "", errors.New("通道没有配置支付方式")
			}
			a, act, err := s.StartPay(ctx, o, types[0].Code, "wap")
			att = a
			if err != nil {
				var last store.Attempt
				if s.DB.Where("order_id = ? AND status = ?", o.ID, store.AttemptFailed).Order("id DESC").Take(&last).Error == nil && last.Error != "" {
					return "", errors.New(last.Error)
				}
				return "", err
			}
			return fmt.Sprintf("%s 下单成功，上游单号 %s，受理类型 %s → %s", types[0].Name, a.UpstreamTradeNo, a.RespType, act.Kind), nil
		})
		step("上游查单（/api/pay/query）", false, func() (string, error) {
			if att == nil {
				return "", errors.New("上一步没有创建上游订单")
			}
			cl, err := s.attemptClient(att)
			if err != nil {
				return "", err
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			r, err := cl.Query(cctx, att.UpstreamOutTradeNo)
			if err != nil {
				return "", err
			}
			return "trade_status=" + r.TradeStatus + " money=" + r.Money, nil
		})
		step("关闭上游订单（/api/pay/close）", false, func() (string, error) {
			if att == nil {
				return "", errors.New("上一步没有创建上游订单")
			}
			cl, err := s.attemptClient(att)
			if err != nil {
				return "", err
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := cl.Close(cctx, att.UpstreamOutTradeNo); err != nil {
				return "", err
			}
			s.DB.Model(att).Update("status", store.AttemptClosed)
			store.Log(s.DB, o.ID, "close", "自检：已关闭上游订单 "+att.UpstreamOutTradeNo)
			return "已关闭 " + att.UpstreamOutTradeNo, nil
		})
	}
	step("模拟支付成功（不经过上游、不扣款）", true, func() (string, error) {
		if err := s.SimulatePay(o); err != nil {
			return "", err
		}
		return "订单已标记为已支付，开始通知 VPN", nil
	})
	step("模拟 VPN 收到支付结果通知（验签、解密、核对金额）", false, func() (string, error) {
		start := time.Now()
		for {
			var n store.TestNotice
			if s.DB.Where("run_id = ?", run.ID).Order("id DESC").Take(&n).Error == nil {
				if n.Code != "SUCCESS" {
					return "", fmt.Errorf("模拟 VPN 回复 %s：%s", n.Code, n.Detail)
				}
				return fmt.Sprintf("%.1f 秒后收到 status=%s amount=%d trade_no=%s", time.Since(start).Seconds(), n.Status, n.Amount, n.TradeNo), nil
			}
			if time.Since(start) > 10*time.Second {
				last := ""
				if f, err := s.OrderByTradeNo(o.TradeNo); err == nil {
					last = f.LastNotifyResult
				}
				return "", fmt.Errorf("10 秒内没有收到通知（最近一次结果：%s）", last)
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	})
	step("模拟 VPN 查询：应为 PAID", false, func() (string, error) {
		t := s.TestQuery(ctx, merchantID, run.OutTradeNo)
		if t.Err != "" {
			return "", errors.New(t.Err)
		}
		r := t.Order
		if r.Status != vpnproto.StatusPaid || r.Amount != cents || r.PaidAt == 0 || r.TradeNo == "" {
			return "", fmt.Errorf("得到 status=%s amount=%d paid_at=%d", r.Status, r.Amount, r.PaidAt)
		}
		return fmt.Sprintf("status=PAID amount=%d trade_no=%s", r.Amount, r.TradeNo), nil
	})
	step("重放同一个查询请求应被拒绝", false, func() (string, error) {
		_, codec, err := s.merchantCodec(merchantID)
		if err != nil {
			return "", err
		}
		env, _ := codec.Seal(vpnproto.Order{OutTradeNo: run.OutTradeNo}, time.Now())
		b, _ := json.Marshal(env)
		if r := s.HandleVPNQuery(ctx, b, "自检"); r.Status != http.StatusOK {
			return "", fmt.Errorf("第一次请求就失败了：HTTP %d", r.Status)
		}
		r := s.HandleVPNQuery(ctx, b, "自检")
		if r.Status != http.StatusUnauthorized {
			return "", fmt.Errorf("重放请求返回 HTTP %d，应为 401", r.Status)
		}
		code := ""
		if m, ok := r.Body.(map[string]string); ok {
			code = m["code"]
		}
		return "第二次返回 HTTP 401 " + code, nil
	})

	passed := true
	for _, st := range steps {
		if !st.OK {
			passed = false
		}
	}
	rb, _ := json.Marshal(steps)
	run.Result, run.Passed = string(rb), passed
	if err := s.DB.Model(run).Updates(map[string]any{"result": run.Result, "passed": passed}).Error; err != nil {
		return run, err
	}
	return run, nil
}

// Steps decodes a run's self-test result.
func Steps(run *store.TestRun) []Step {
	var st []Step
	if run.Result != "" {
		_ = json.Unmarshal([]byte(run.Result), &st)
	}
	return st
}
