package payment

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Gateway is the "下单链接" integration: the simplest way for a payment
// center to plug in.
//
//  1. Order: the app opens PayLink?<signed params>. The URL carries the order
//     number, amount and our callback URLs, signed with HMAC-SHA256 so the
//     amount can't be edited in the address bar.
//  2. Notify: after payment the center POSTs an encrypted, signed envelope to
//     our fixed notify URL.
//  3. Query: the center may POST to our fixed query URL to read an order
//     (status, amount) before charging or to reconcile.
//
// Envelopes use the same AES-256-GCM + HMAC-SHA256 scheme as the cashier
// protocol (see Cashier.Seal / Open): timestamp window, single-use nonce,
// constant-time signature check, authenticated decryption.
type Gateway struct {
	PayLink string
	codec   *Cashier
}

// GatewayOrder is the decrypted payload of notify / query.
type GatewayOrder struct {
	OutTradeNo string `json:"out_trade_no"`
	TradeNo    string `json:"trade_no,omitempty"`
	Amount     int64  `json:"amount"` // cents
	Currency   string `json:"currency,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Status     string `json:"status,omitempty"` // PENDING | PAID
	CreatedAt  int64  `json:"created_at,omitempty"`
	PaidAt     int64  `json:"paid_at,omitempty"`
}

func NewGateway(payLink, merchantID, signKeyHex, encKeyHex string) (*Gateway, error) {
	sk, err := hex.DecodeString(signKeyHex)
	if err != nil || len(sk) < 16 {
		return nil, errors.New("gateway: sign_key must be hex, at least 16 bytes")
	}
	ek, err := hex.DecodeString(encKeyHex)
	if err != nil || len(ek) != 32 {
		return nil, errors.New("gateway: enc_key must be hex, exactly 32 bytes")
	}
	if merchantID == "" {
		return nil, errors.New("gateway: merchant_id required")
	}
	return &Gateway{PayLink: payLink, codec: &Cashier{MerchantID: merchantID, SignKey: sk, EncKey: ek}}, nil
}

// NewKeys generates a merchant id and fresh keys (hex).
func NewKeys() (merchantID, signKey, encKey string, err error) {
	b := make([]byte, 4+32+32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	return "HY" + strings.ToUpper(hex.EncodeToString(b[:4])), hex.EncodeToString(b[4:36]), hex.EncodeToString(b[36:]), nil
}

func (g *Gateway) MerchantID() string { return g.codec.MerchantID }

// Canonical is the string the link signature covers: keys sorted, "k=v"
// joined with "&", raw (not URL-escaped) values, sign itself excluded.
func Canonical(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "sign" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k + "=" + params[k])
	}
	return b.String()
}

func (g *Gateway) signParams(p map[string]string) string {
	m := hmac.New(sha256.New, g.codec.SignKey)
	m.Write([]byte(Canonical(p)))
	return hex.EncodeToString(m.Sum(nil))
}

// LinkParams are the signed parameters appended to PayLink.
func (g *Gateway) LinkParams(o Order, notifyURL, queryURL, returnURL string, now time.Time) (map[string]string, error) {
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return nil, err
	}
	p := map[string]string{
		"merchant_id": g.codec.MerchantID, "out_trade_no": o.ID, "amount": strconv.FormatInt(o.AmountCents, 10),
		"currency": "CNY", "subject": o.Subject, "notify_url": notifyURL, "query_url": queryURL,
		"return_url": returnURL, "timestamp": strconv.FormatInt(now.Unix(), 10),
		"expire_at": strconv.FormatInt(now.Add(orderTTL).Unix(), 10), "nonce": hex.EncodeToString(nb),
	}
	p["sign"] = g.signParams(p)
	return p, nil
}

// PayURL is PayLink with the signed order parameters.
func (g *Gateway) PayURL(o Order, notifyURL, queryURL, returnURL string) (string, error) {
	p, err := g.LinkParams(o, notifyURL, queryURL, returnURL, time.Now())
	if err != nil {
		return "", err
	}
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	sep := "?"
	if strings.Contains(g.PayLink, "?") {
		sep = "&"
	}
	return g.PayLink + sep + q.Encode(), nil
}

// VerifyLink checks a link's signature (what the payment center must do).
func (g *Gateway) VerifyLink(q url.Values, now time.Time) error {
	p := map[string]string{}
	for k := range q {
		p[k] = q.Get(k)
	}
	want, _ := hex.DecodeString(g.signParams(p))
	got, err := hex.DecodeString(p["sign"])
	if err != nil || !hmac.Equal(want, got) {
		return ErrBadSignature
	}
	exp, _ := strconv.ParseInt(p["expire_at"], 10, 64)
	if now.Unix() > exp {
		return fmt.Errorf("gateway: link expired")
	}
	return nil
}

// Open verifies and decrypts an envelope from the payment center.
func (g *Gateway) Open(env *Envelope, v any) error { return g.codec.Open(env, v, time.Now()) }

// Seal encrypts and signs a response to the payment center.
func (g *Gateway) Seal(v any) (*Envelope, error) { return g.codec.Seal(v, time.Now()) }
