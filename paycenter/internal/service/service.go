// Package service is the paycenter's business logic: it sits between the VPN
// system ("下单链接" protocol) and the upstream payment platform (信联pay).
//
//	VPN app ──link──> /checkout (verify) ──> cashier page ──> 信联pay /api/pay/submit
//	信联pay ──form callback──> /notify/xinlian/{channel} ──> order PAID ──> sealed notify ──> VPN
//	VPN ──sealed query──> /api/order/query (refreshes from 信联pay /api/pay/query when pending)
package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"paycenter/internal/store"
	"paycenter/internal/vpnproto"
	"paycenter/internal/xinlian"
)

// Setting keys.
const (
	SettingPublicURL  = "public_url"
	SettingPageSecret = "page_secret"
)

// Service is shared by the HTTP handlers and the background workers.
type Service struct {
	DB     *gorm.DB
	Nonces vpnproto.NonceCache
	// testNonces: replay protection of the built-in "模拟 VPN" receiver.
	testNonces vpnproto.NonceCache
	HTTP       *http.Client // VPN notifications

	kick chan struct{}

	mu       sync.Mutex
	clients  map[uint]cachedClient
	syncing  sync.Map // order id -> struct{}
	secretMu sync.Mutex
}

type cachedClient struct {
	updated time.Time
	c       *xinlian.Client
}

func New(db *gorm.DB) *Service {
	return &Service{DB: db, HTTP: &http.Client{Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		kick: make(chan struct{}, 1), clients: map[uint]cachedClient{}}
}

// UserError is shown to the user as is.
type UserError struct {
	Status int
	Msg    string
}

func (e *UserError) Error() string { return e.Msg }

func userErr(status int, format string, a ...any) error {
	return &UserError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// PublicURL is this center's external base URL (no trailing slash).
func (s *Service) PublicURL() string {
	return strings.TrimRight(store.GetSetting(s.DB, SettingPublicURL), "/")
}

// Secret returns a random per-install secret, creating it on first use.
func (s *Service) Secret(name string) []byte {
	s.secretMu.Lock()
	defer s.secretMu.Unlock()
	v := store.GetSetting(s.DB, name)
	if b, err := hex.DecodeString(v); err == nil && len(b) >= 32 {
		return b
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	if err := store.SetSetting(s.DB, name, hex.EncodeToString(b)); err != nil {
		slog.Error("save secret", "name", name, "err", err)
	}
	return b
}

// PageToken authorizes cashier pages for one order.
func (s *Service) PageToken(tradeNo string) string {
	m := hmac.New(sha256.New, s.Secret(SettingPageSecret))
	m.Write([]byte("page|" + tradeNo))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// CheckPageToken compares in constant time.
func (s *Service) CheckPageToken(tradeNo, tok string) bool {
	return hmac.Equal([]byte(s.PageToken(tradeNo)), []byte(tok))
}

// Codec builds the protocol codec of a merchant.
func Codec(m *store.Merchant) (*vpnproto.Codec, error) {
	return vpnproto.NewCodec(m.MerchantID, m.SignKey, m.EncKey)
}

// Client returns the (cached) upstream client of a channel.
func (s *Service) Client(ch *store.Channel) (*xinlian.Client, error) {
	ref := s.PublicURL()
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[ch.ID]; ok && c.updated.Equal(ch.UpdatedAt) && c.c.Referer == strings.TrimRight(ref, "/") {
		return c.c, nil
	}
	c, err := NewClient(ch, ref)
	if err != nil {
		return nil, err
	}
	s.clients[ch.ID] = cachedClient{updated: ch.UpdatedAt, c: c}
	return c, nil
}

// NewClient builds an upstream client from a channel row. referer is this
// center's public URL, sent as Referer / Origin (信联pay checks it against
// the merchant's authorized domains).
func NewClient(ch *store.Channel, referer string) (*xinlian.Client, error) {
	return xinlian.New(xinlian.Config{BaseURL: ch.APIBase, PID: ch.PID, SignType: ch.SignType,
		MD5Key: ch.MD5Key, RSAPrivateKey: ch.RSAPrivateKey, SignWithType: ch.SignWithType, Referer: referer})
}

func (s *Service) merchant(merchantID string) (*store.Merchant, error) {
	var m store.Merchant
	if err := s.DB.Where("merchant_id = ?", merchantID).Take(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

func (s *Service) channel(id uint) (*store.Channel, error) {
	var ch store.Channel
	if err := s.DB.Take(&ch, id).Error; err != nil {
		return nil, err
	}
	return &ch, nil
}

// newNo makes an order number: prefix + yyyymmddHHMMSS + random hex.
func newNo(prefix string, randBytes int) string {
	b := make([]byte, randBytes)
	_, _ = rand.Read(b)
	return prefix + time.Now().Format("20060102150405") + strings.ToUpper(hex.EncodeToString(b))
}

func errNotFound(err error) bool { return errors.Is(err, gorm.ErrRecordNotFound) }

// Kick wakes the notify worker.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}
