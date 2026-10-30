package payment

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Epay implements the widely used "易支付" aggregator protocol
// (submit.php, MD5 signature). Supports alipay / wxpay / qqpay via the aggregator.
type Epay struct {
	APIURL string
	PID    string
	Key    string
	Types  []string
}

var epayNames = map[string]string{"alipay": "支付宝", "wxpay": "微信支付", "qqpay": "QQ 钱包"}

func (e *Epay) Name() string { return "epay" }
func (e *Epay) Ack() string  { return "success" }

func (e *Epay) Methods() []Method {
	out := make([]Method, 0, len(e.Types))
	for _, t := range e.Types {
		name := epayNames[t]
		if name == "" {
			name = t
		}
		out = append(out, Method{ID: "epay." + t, Name: name})
	}
	return out
}

func (e *Epay) PayURL(o Order, method, notifyURL, returnURL string) (string, error) {
	typ := strings.TrimPrefix(method, "epay.")
	p := map[string]string{
		"pid":          e.PID,
		"type":         typ,
		"out_trade_no": o.ID,
		"notify_url":   notifyURL,
		"return_url":   returnURL,
		"name":         o.Subject,
		"money":        Yuan(o.AmountCents),
	}
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	q.Set("sign", e.sign(p))
	q.Set("sign_type", "MD5")
	return strings.TrimRight(e.APIURL, "/") + "/submit.php?" + q.Encode(), nil
}

func (e *Epay) ParseNotify(r *http.Request) (*Notification, error) {
	p, err := formParams(r)
	if err != nil {
		return nil, err
	}
	want := e.sign(p)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(p["sign"])), []byte(want)) != 1 {
		return nil, ErrBadSignature
	}
	if p["pid"] != e.PID {
		return nil, errors.New("epay: pid mismatch")
	}
	amount, err := ParseYuan(p["money"])
	if err != nil {
		return nil, err
	}
	return &Notification{
		OrderID: p["out_trade_no"], TradeNo: p["trade_no"], AmountCents: amount,
		Paid: p["trade_status"] == "TRADE_SUCCESS",
	}, nil
}

// sign = md5(sorted non-empty k=v joined by & (excluding sign, sign_type) + key)
func (e *Epay) sign(p map[string]string) string {
	sum := md5.Sum([]byte(signContent(p, "sign", "sign_type") + e.Key))
	return hex.EncodeToString(sum[:])
}
