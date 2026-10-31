package payment

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cashier implements the "收银台 v1" protocol (docs/payment-api.md): a hosted
// checkout that we integrate with over an encrypted, signed JSON API.
//
// Every message in both directions is an Envelope:
//
//	data = base64( iv(12) || AES-256-GCM(enc_key, iv, plaintext JSON, aad=merchant_id) )
//	sign = hex( HMAC-SHA256(sign_key, merchant_id \n timestamp \n nonce \n data) )
//
// Receivers reject a timestamp more than 5 minutes off, a reused nonce, a bad
// signature or a payload that fails authenticated decryption.
type Cashier struct {
	APIURL     string // e.g. https://pay.example.com
	MerchantID string
	SignKey    []byte // HMAC key
	EncKey     []byte // 32 bytes, AES-256
	Channels   []Method
	HTTP       *http.Client

	nonces nonceCache
}

// Envelope is the wire format for every cashier request, response and callback.
type Envelope struct {
	MerchantID string `json:"merchant_id"`
	Timestamp  int64  `json:"timestamp"`
	Nonce      string `json:"nonce"`
	Data       string `json:"data"`
	Sign       string `json:"sign"`
}

// Order status values used by the protocol.
const (
	StatusPending  = "PENDING"
	StatusPaid     = "PAID"
	StatusClosed   = "CLOSED"
	StatusRefunded = "REFUNDED"
)

// cashierOrder is the decrypted payload of create / query / notify.
type cashierOrder struct {
	OutTradeNo string `json:"out_trade_no"`
	TradeNo    string `json:"trade_no,omitempty"`
	Amount     int64  `json:"amount"` // cents
	Currency   string `json:"currency,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Channel    string `json:"channel,omitempty"`
	NotifyURL  string `json:"notify_url,omitempty"`
	ReturnURL  string `json:"return_url,omitempty"`
	ExpireAt   int64  `json:"expire_at,omitempty"`
	Status     string `json:"status,omitempty"`
	PayURL     string `json:"pay_url,omitempty"`
	PaidAt     int64  `json:"paid_at,omitempty"`
}

const (
	clockSkew  = 5 * time.Minute
	orderTTL   = 30 * time.Minute
	httpTimout = 12 * time.Second
)

// NewCashier validates keys (base64 or hex) and builds the provider.
func NewCashier(apiURL, merchantID, signKey, encKey string, channels []Method) (*Cashier, error) {
	sk, err := decodeKey(signKey)
	if err != nil || len(sk) < 16 {
		return nil, errors.New("cashier: sign_key must be at least 16 bytes (base64 or hex)")
	}
	ek, err := decodeKey(encKey)
	if err != nil || len(ek) != 32 {
		return nil, errors.New("cashier: enc_key must be exactly 32 bytes (base64 or hex) for AES-256")
	}
	if !strings.HasPrefix(apiURL, "https://") && !strings.HasPrefix(apiURL, "http://127.") {
		return nil, errors.New("cashier: api_url must be https")
	}
	if len(channels) == 0 {
		channels = []Method{{ID: "alipay", Name: "支付宝"}, {ID: "wxpay", Name: "微信支付"}}
	}
	return &Cashier{
		APIURL: strings.TrimRight(apiURL, "/"), MerchantID: merchantID,
		SignKey: sk, EncKey: ek, Channels: channels,
		HTTP: &http.Client{Timeout: httpTimout},
	}, nil
}

func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(s)%2 == 0 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawURLEncoding.DecodeString(s)
}

func (c *Cashier) Name() string { return "cashier" }

// Ack is our reply body to a valid callback.
func (c *Cashier) Ack() string { return `{"code":"SUCCESS"}` }

func (c *Cashier) Methods() []Method {
	out := make([]Method, 0, len(c.Channels))
	for _, m := range c.Channels {
		out = append(out, Method{ID: "cashier." + m.ID, Name: m.Name})
	}
	return out
}

// PayURL creates the order at the cashier and returns its checkout URL.
func (c *Cashier) PayURL(o Order, method, notifyURL, returnURL string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), httpTimout)
	defer cancel()
	var resp cashierOrder
	err := c.call(ctx, "/v1/orders/create", cashierOrder{
		OutTradeNo: o.ID, Amount: o.AmountCents, Currency: "CNY", Subject: o.Subject,
		Channel: strings.TrimPrefix(method, "cashier."), NotifyURL: notifyURL, ReturnURL: returnURL,
		ExpireAt: time.Now().Add(orderTTL).Unix(),
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.OutTradeNo != o.ID || resp.Amount != o.AmountCents {
		return "", errors.New("cashier: create response does not match the order")
	}
	if !strings.HasPrefix(resp.PayURL, "https://") {
		return "", errors.New("cashier: pay_url must be https")
	}
	return resp.PayURL, nil
}

// Query asks the cashier for an order's current status (active polling, used
// when a callback is late or lost).
func (c *Cashier) Query(ctx context.Context, orderID string) (*Notification, error) {
	var resp cashierOrder
	if err := c.call(ctx, "/v1/orders/query", cashierOrder{OutTradeNo: orderID}, &resp); err != nil {
		return nil, err
	}
	if resp.OutTradeNo != orderID {
		return nil, errors.New("cashier: query response does not match the order")
	}
	return &Notification{OrderID: resp.OutTradeNo, TradeNo: resp.TradeNo, AmountCents: resp.Amount,
		Paid: resp.Status == StatusPaid}, nil
}

// ParseNotify verifies and decrypts a payment callback.
func (c *Cashier) ParseNotify(r *http.Request) (*Notification, error) {
	var o cashierOrder
	if err := c.OpenRequest(r, &o); err != nil {
		return nil, err
	}
	if o.OutTradeNo == "" {
		return nil, errors.New("cashier: missing out_trade_no")
	}
	if o.Currency != "" && o.Currency != "CNY" {
		return nil, fmt.Errorf("cashier: unexpected currency %q", o.Currency)
	}
	return &Notification{OrderID: o.OutTradeNo, TradeNo: o.TradeNo, AmountCents: o.Amount,
		Paid: o.Status == StatusPaid}, nil
}

// OpenRequest reads, verifies and decrypts an Envelope from an HTTP request into v.
func (c *Cashier) OpenRequest(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		return err
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("cashier: bad envelope: %w", err)
	}
	return c.Open(&env, v, time.Now())
}

// Seal encrypts and signs v into an Envelope.
func (c *Cashier) Seal(v any, now time.Time) (*Envelope, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(c.EncKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	ct := gcm.Seal(iv, iv, plain, []byte(c.MerchantID))
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return nil, err
	}
	env := &Envelope{MerchantID: c.MerchantID, Timestamp: now.Unix(), Nonce: hex.EncodeToString(nb),
		Data: base64.StdEncoding.EncodeToString(ct)}
	env.Sign = c.sign(env)
	return env, nil
}

// Open verifies and decrypts an Envelope into v.
func (c *Cashier) Open(env *Envelope, v any, now time.Time) error {
	if env.MerchantID != c.MerchantID {
		return errors.New("cashier: merchant mismatch")
	}
	if d := now.Sub(time.Unix(env.Timestamp, 0)); d > clockSkew || d < -clockSkew {
		return errors.New("cashier: timestamp outside the allowed window")
	}
	if len(env.Nonce) < 16 || len(env.Nonce) > 64 {
		return errors.New("cashier: bad nonce")
	}
	want, _ := hex.DecodeString(c.sign(env))
	got, err := hex.DecodeString(env.Sign)
	if err != nil || !hmac.Equal(want, got) {
		return ErrBadSignature
	}
	// Only after the signature checks out, so forged requests can't fill the cache.
	if !c.nonces.add(env.Nonce, now) {
		return errors.New("cashier: replayed nonce")
	}
	raw, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return errors.New("cashier: bad data encoding")
	}
	block, err := aes.NewCipher(c.EncKey)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return errors.New("cashier: data too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(c.MerchantID))
	if err != nil {
		return errors.New("cashier: decryption failed")
	}
	return json.Unmarshal(plain, v)
}

func (c *Cashier) sign(env *Envelope) string {
	m := hmac.New(sha256.New, c.SignKey)
	fmt.Fprintf(m, "%s\n%d\n%s\n%s", env.MerchantID, env.Timestamp, env.Nonce, env.Data)
	return hex.EncodeToString(m.Sum(nil))
}

// call POSTs a sealed request and opens the sealed response. Non-2xx responses
// carry a plain {"code","message"} error body.
func (c *Cashier) call(ctx context.Context, path string, req, resp any) error {
	return c.callURL(ctx, c.APIURL+path, path, req, resp)
}

// callURL is call against a full URL; label names it in errors.
func (c *Cashier) callURL(ctx context.Context, target, path string, req, resp any) error {
	env, err := c.Seal(req, time.Now())
	if err != nil {
		return err
	}
	b, _ := json.Marshal(env)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(b))
	if err != nil {
		return err
	}
	hr.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(hr)
	if err != nil {
		return fmt.Errorf("cashier: %s: %w", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
	if res.StatusCode/100 != 2 {
		var e struct{ Code, Message string }
		_ = json.Unmarshal(body, &e)
		return fmt.Errorf("cashier: %s: HTTP %s %s %s", path, strconv.Itoa(res.StatusCode), e.Code, e.Message)
	}
	var out Envelope
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("cashier: %s: bad response: %w", path, err)
	}
	return c.Open(&out, resp, time.Now())
}

// nonceCache remembers nonces for the replay window.
type nonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (n *nonceCache) add(nonce string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]time.Time{}
	}
	if len(n.seen) > 4096 {
		for k, t := range n.seen {
			if now.Sub(t) > 2*clockSkew {
				delete(n.seen, k)
			}
		}
	}
	if _, dup := n.seen[nonce]; dup {
		return false
	}
	n.seen[nonce] = now
	return true
}

// Querier is implemented by providers that support active status queries.
type Querier interface {
	Query(ctx context.Context, orderID string) (*Notification, error)
}
