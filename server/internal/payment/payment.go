// Package payment implements payment providers. Each provider builds a URL
// the user opens to pay and verifies the asynchronous payment notification.
package payment

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Method is a payment option shown in the app, e.g. {ID: "epay.wxpay", Name: "微信支付"}.
type Method struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Order is what a provider needs to know about an order.
type Order struct {
	ID          string
	AmountCents int64
	Subject     string
}

// Notification is a verified payment callback.
type Notification struct {
	OrderID     string
	TradeNo     string
	AmountCents int64
	Paid        bool // false for e.g. WAIT_BUYER_PAY notifications
}

type Provider interface {
	Name() string // URL segment: /pay/notify/{Name}
	Methods() []Method
	// PayURL returns the URL that starts payment for method (one of Methods()).
	PayURL(o Order, method, notifyURL, returnURL string) (string, error)
	// ParseNotify verifies the signature of a provider callback.
	ParseNotify(r *http.Request) (*Notification, error)
	// Ack is the body the provider expects after a successful notification.
	Ack() string
}

var ErrBadSignature = errors.New("payment: bad signature")

// Yuan formats cents as "25.00".
func Yuan(cents int64) string { return fmt.Sprintf("%d.%02d", cents/100, cents%100) }

// ParseYuan parses "25", "25.0" or "25.00" into cents without floating point.
func ParseYuan(s string) (int64, error) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if len(frac) > 2 {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad amount %q", s)
	}
	return w*100 + f, nil
}

// formParams reads GET query and POST form values into a flat map.
func formParams(r *http.Request) (map[string]string, error) {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	m := map[string]string{}
	for k, v := range r.Form {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m, nil
}
