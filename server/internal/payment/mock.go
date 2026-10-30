package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
)

// Mock is a fake provider for development: the pay URL is a page on this API
// with a "pay" button. Completing it is authorized with an HMAC of the order
// ID, so it can't be used to mark arbitrary orders paid. Never enable in production.
type Mock struct {
	PublicURL string
	Secret    []byte
}

func (m *Mock) Name() string      { return "mock" }
func (m *Mock) Ack() string       { return "success" }
func (m *Mock) Methods() []Method { return []Method{{ID: "mock", Name: "模拟支付（测试）"}} }

func (m *Mock) Token(orderID string) string {
	h := hmac.New(sha256.New, m.Secret)
	h.Write([]byte("mock-pay:" + orderID))
	return hex.EncodeToString(h.Sum(nil))
}

func (m *Mock) PayURL(o Order, _ string, _, _ string) (string, error) {
	return m.PublicURL + "/pay/mock/" + url.PathEscape(o.ID) + "?t=" + m.Token(o.ID), nil
}

// ParseNotify handles POST /pay/notify/mock with order_id, amount_cents, t.
func (m *Mock) ParseNotify(r *http.Request) (*Notification, error) {
	p, err := formParams(r)
	if err != nil {
		return nil, err
	}
	id := p["order_id"]
	if !hmac.Equal([]byte(p["t"]), []byte(m.Token(id))) {
		return nil, ErrBadSignature
	}
	amount, err := ParseYuan(p["amount"])
	if err != nil {
		return nil, errors.New("mock: bad amount")
	}
	return &Notification{OrderID: id, TradeNo: "MOCK-" + id, AmountCents: amount, Paid: true}, nil
}
