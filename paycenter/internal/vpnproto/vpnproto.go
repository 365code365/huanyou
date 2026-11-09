// Package vpnproto implements the payment center's side of the VPN system's
// "下单链接" protocol (server/internal/api/payment_doc.md):
//
//   - the checkout link the app opens is signed with
//     hex(HMAC-SHA256(sign_key, sorted "k=v&..." of every param except sign));
//   - notifications (center -> VPN) and order queries (VPN -> center) are
//     JSON envelopes: data = base64(iv(12) || AES-256-GCM(enc_key, iv, JSON,
//     aad=merchant_id)), sign = hex(HMAC-SHA256(sign_key,
//     merchant_id\ntimestamp\nnonce\ndata)).
//
// It is wire-compatible with server/internal/payment (Gateway / Cashier).
package vpnproto

import (
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
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Order status values of the protocol.
const (
	StatusPending = "PENDING"
	StatusPaid    = "PAID"
	StatusClosed  = "CLOSED"
)

const (
	// ClockSkew is the accepted |now - timestamp| of an envelope.
	ClockSkew = 5 * time.Minute
	// NonceTTL is how long a used nonce is remembered (replay protection).
	NonceTTL = 10 * time.Minute
)

var (
	ErrBadSignature = errors.New("bad signature")
	ErrReplay       = errors.New("replayed nonce")
)

// Envelope is the wire format of notify / query requests and responses.
type Envelope struct {
	MerchantID string `json:"merchant_id"`
	Timestamp  int64  `json:"timestamp"`
	Nonce      string `json:"nonce"`
	Data       string `json:"data"`
	Sign       string `json:"sign"`
}

// Order is the decrypted payload of notify and query.
type Order struct {
	OutTradeNo string `json:"out_trade_no"`
	TradeNo    string `json:"trade_no,omitempty"`
	Amount     int64  `json:"amount"` // cents
	Currency   string `json:"currency,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Status     string `json:"status,omitempty"`
	CreatedAt  int64  `json:"created_at,omitempty"`
	PaidAt     int64  `json:"paid_at,omitempty"`
}

// Codec holds one merchant's id and keys.
type Codec struct {
	MerchantID string
	signKey    []byte
	encKey     []byte
}

// NewCodec validates hex keys: sign_key >= 16 bytes, enc_key exactly 32 bytes.
func NewCodec(merchantID, signKeyHex, encKeyHex string) (*Codec, error) {
	if strings.TrimSpace(merchantID) == "" {
		return nil, errors.New("merchant_id 不能为空")
	}
	sk, err := hex.DecodeString(strings.TrimSpace(signKeyHex))
	if err != nil || len(sk) < 16 {
		return nil, errors.New("签名密钥必须是 hex，至少 16 字节（32 个字符）")
	}
	ek, err := hex.DecodeString(strings.TrimSpace(encKeyHex))
	if err != nil || len(ek) != 32 {
		return nil, errors.New("加密密钥必须是 hex，正好 32 字节（64 个字符）")
	}
	return &Codec{MerchantID: merchantID, signKey: sk, encKey: ek}, nil
}

// GenerateKeys returns a merchant id and fresh keys in the VPN system's format.
func GenerateKeys() (merchantID, signKey, encKey string, err error) {
	b := make([]byte, 4+32+32)
	if _, err = rand.Read(b); err != nil {
		return
	}
	return "HY" + strings.ToUpper(hex.EncodeToString(b[:4])), hex.EncodeToString(b[4:36]), hex.EncodeToString(b[36:]), nil
}

// Canonical is the string the link signature covers: keys sorted, raw values,
// "sign" excluded, empty values kept.
func Canonical(p map[string]string) string {
	keys := make([]string, 0, len(p))
	for k := range p {
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
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(p[k])
	}
	return b.String()
}

// SignLink signs link parameters.
func (c *Codec) SignLink(p map[string]string) string {
	m := hmac.New(sha256.New, c.signKey)
	m.Write([]byte(Canonical(p)))
	return hex.EncodeToString(m.Sum(nil))
}

// VerifyLink checks the merchant id, the signature and expire_at of a checkout link.
func (c *Codec) VerifyLink(q url.Values, now time.Time) error {
	p := make(map[string]string, len(q))
	for k := range q {
		p[k] = q.Get(k)
	}
	if p["merchant_id"] != c.MerchantID {
		return errors.New("商户号不匹配")
	}
	want, _ := hex.DecodeString(c.SignLink(p))
	got, err := hex.DecodeString(strings.ToLower(p["sign"]))
	if err != nil || !hmac.Equal(want, got) {
		return ErrBadSignature
	}
	exp, err := strconv.ParseInt(p["expire_at"], 10, 64)
	if err != nil || now.Unix() > exp {
		return errors.New("链接已过期")
	}
	return nil
}

// Seal encrypts and signs v with a fresh timestamp and nonce.
func (c *Codec) Seal(v any, now time.Time) (*Envelope, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	gcm, err := c.gcm()
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

// Open verifies (merchant, timestamp window, signature, single-use nonce) and
// decrypts an envelope into v. nonces may be nil to skip the replay check.
func (c *Codec) Open(env *Envelope, v any, nonces *NonceCache, now time.Time) error {
	if env.MerchantID != c.MerchantID {
		return errors.New("merchant mismatch")
	}
	if d := now.Sub(time.Unix(env.Timestamp, 0)); d > ClockSkew || d < -ClockSkew {
		return errors.New("timestamp outside the allowed window")
	}
	if len(env.Nonce) < 16 || len(env.Nonce) > 64 {
		return errors.New("bad nonce")
	}
	want, _ := hex.DecodeString(c.sign(env))
	got, err := hex.DecodeString(strings.ToLower(env.Sign))
	if err != nil || !hmac.Equal(want, got) {
		return ErrBadSignature
	}
	// After the signature check, so forged requests can't fill the cache.
	if nonces != nil && !nonces.Add(env.MerchantID+"|"+env.Nonce, now) {
		return ErrReplay
	}
	raw, err := base64.StdEncoding.DecodeString(env.Data)
	if err != nil {
		return errors.New("bad data encoding")
	}
	gcm, err := c.gcm()
	if err != nil {
		return err
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return errors.New("data too short")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], []byte(c.MerchantID))
	if err != nil {
		return errors.New("decryption failed")
	}
	return json.Unmarshal(plain, v)
}

func (c *Codec) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.encKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (c *Codec) sign(env *Envelope) string {
	m := hmac.New(sha256.New, c.signKey)
	fmt.Fprintf(m, "%s\n%d\n%s\n%s", env.MerchantID, env.Timestamp, env.Nonce, env.Data)
	return hex.EncodeToString(m.Sum(nil))
}

// NonceCache remembers used nonces for NonceTTL. It is per process: run a
// single paycenter instance, or put a shared store behind it when scaling out.
type NonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
	last time.Time
}

// Add records a nonce; false if it was seen within NonceTTL.
func (n *NonceCache) Add(key string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.seen == nil {
		n.seen = map[string]time.Time{}
	}
	if now.Sub(n.last) > time.Minute {
		for k, t := range n.seen {
			if now.Sub(t) > NonceTTL {
				delete(n.seen, k)
			}
		}
		n.last = now
	}
	if t, dup := n.seen[key]; dup && now.Sub(t) <= NonceTTL {
		return false
	}
	n.seen[key] = now
	return true
}
