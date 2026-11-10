// Package store holds the paycenter's data (GORM; SQLite by default, MySQL by
// switching the driver and DSN).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Order status (stored). An unpaid order past ExpireAt is reported as CLOSED.
const (
	OrderPending = "PENDING"
	OrderPaid    = "PAID"
)

// Notify status of the paid -> VPN notification.
const (
	NotifyNone    = ""
	NotifyPending = "PENDING" // due at NextNotifyAt
	NotifySuccess = "SUCCESS"
	NotifyFailed  = "FAILED" // gave up after 24 h; resend from the admin panel
)

// Attempt status (one upstream order per "pay with X" click).
const (
	AttemptPending = "PENDING"
	AttemptPaid    = "PAID"
	AttemptClosed  = "CLOSED"
	AttemptFailed  = "FAILED" // submit rejected
)

// Setting is a key/value row (public URL, admin password hash, secrets).
type Setting struct {
	Key   string `gorm:"primaryKey;size:64"`
	Value string `gorm:"type:text"`
}

// Channel is an upstream payment account (信联pay merchant).
type Channel struct {
	ID            uint   `gorm:"primaryKey"`
	Name          string `gorm:"size:64;not null"`
	Provider      string `gorm:"size:32;not null;default:xinlian"`
	APIBase       string `gorm:"size:255;not null"`
	PID           string `gorm:"size:64;not null"`
	SignType      string `gorm:"size:8;not null;default:MD5"`
	MD5Key        string `gorm:"size:255"`
	RSAPrivateKey string `gorm:"type:text"`
	SignWithType  bool   `gorm:"not null;default:false"`
	PayTypes      string `gorm:"type:text"` // JSON []PayType; legacy: one "code=显示名" per line
	Device        string `gorm:"size:16"`
	Enabled       bool   `gorm:"not null;default:true"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PayType is a payment method of a channel. Hidden ones (e.g. under
// maintenance) stay configured but are not offered on the cashier.
type PayType struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Hidden bool   `json:"hidden,omitempty"`
	Remark string `json:"remark,omitempty"`
}

// EncodePayTypes serializes pay types for Channel.PayTypes.
func EncodePayTypes(list []PayType) string {
	if len(list) == 0 {
		return ""
	}
	b, _ := json.Marshal(list)
	return string(b)
}

// PayTypeList returns every configured pay type, hidden ones included.
func (c Channel) PayTypeList() []PayType {
	s := strings.TrimSpace(c.PayTypes)
	if strings.HasPrefix(s, "[") {
		var list []PayType
		if json.Unmarshal([]byte(s), &list) != nil {
			return nil
		}
		out := list[:0]
		for _, p := range list {
			p.Code, p.Name, p.Remark = strings.TrimSpace(p.Code), strings.TrimSpace(p.Name), strings.TrimSpace(p.Remark)
			if p.Code == "" {
				continue
			}
			if p.Name == "" {
				p.Name = p.Code
			}
			out = append(out, p)
		}
		return out
	}
	var out []PayType
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		code, name, _ := strings.Cut(line, "=")
		code, name = strings.TrimSpace(code), strings.TrimSpace(name)
		if code == "" {
			continue
		}
		if name == "" {
			name = code
		}
		out = append(out, PayType{Code: code, Name: name})
	}
	return out
}

// VisiblePayTypes returns the pay types offered on the cashier.
func (c Channel) VisiblePayTypes() []PayType {
	var out []PayType
	for _, p := range c.PayTypeList() {
		if !p.Hidden {
			out = append(out, p)
		}
	}
	return out
}

// Merchant is a connected VPN system: its merchant id and the two keys from
// the VPN admin panel (系统设置 → 支付).
type Merchant struct {
	ID         uint   `gorm:"primaryKey"`
	Name       string `gorm:"size:64;not null"`
	MerchantID string `gorm:"size:64;not null;uniqueIndex"`
	SignKey    string `gorm:"size:128;not null"`
	EncKey     string `gorm:"size:64;not null"`
	ChannelID  uint   `gorm:"not null;default:0"`
	NotifyURL  string `gorm:"size:512"` // if set, overrides the notify_url in links
	Enabled    bool   `gorm:"not null;default:true"`
	Remark     string `gorm:"size:255"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Order is a VPN order seen at the checkout.
type Order struct {
	ID               uint   `gorm:"primaryKey"`
	TradeNo          string `gorm:"size:32;not null;uniqueIndex"` // ours, reported to the VPN as trade_no
	MerchantID       string `gorm:"size:64;not null;uniqueIndex:uk_orders_merchant_out,priority:1"`
	OutTradeNo       string `gorm:"size:64;not null;uniqueIndex:uk_orders_merchant_out,priority:2"`
	Amount           int64  `gorm:"not null"` // cents
	Currency         string `gorm:"size:8;not null"`
	Subject          string `gorm:"size:255"`
	NotifyURL        string `gorm:"size:512"`
	ReturnURL        string `gorm:"size:1024"`
	ExpireAt         int64  `gorm:"not null"` // unix seconds
	Status           string `gorm:"size:16;not null;index"`
	ChannelID        uint
	PayType          string `gorm:"size:32"`
	UpstreamTradeNo  string `gorm:"size:64"`
	PaidAt           *time.Time
	NotifyStatus     string     `gorm:"size:16;index"`
	NotifyAttempts   int        `gorm:"not null;default:0"`
	NextNotifyAt     *time.Time `gorm:"index"`
	LastNotifyAt     *time.Time
	LastNotifyResult string `gorm:"size:512"`
	LastSyncAt       *time.Time
	ClientIP         string    `gorm:"size:64"`
	IsTest           bool      `gorm:"not null;default:false;index"` // created by 测试下单; notified to the built-in receiver
	CreatedAt        time.Time `gorm:"index"`
	UpdatedAt        time.Time
}

// TestRun is a test order made in the admin panel, playing the VPN side:
// a checkout link signed with the merchant's keys whose notifications go to
// the built-in "模拟 VPN" receiver instead of the real VPN.
type TestRun struct {
	ID         uint      `gorm:"primaryKey"`
	MerchantID string    `gorm:"size:64;not null;uniqueIndex:uk_test_runs_merchant_out,priority:1"`
	OutTradeNo string    `gorm:"size:64;not null;uniqueIndex:uk_test_runs_merchant_out,priority:2"`
	Amount     int64     `gorm:"not null"`
	Subject    string    `gorm:"size:255"`
	PayLink    string    `gorm:"type:text"`
	Kind       string    `gorm:"size:16;not null"` // manual | auto
	Passed     bool      `gorm:"not null;default:false"`
	Result     string    `gorm:"type:text"` // self-test steps (JSON)
	CreatedAt  time.Time `gorm:"index"`
}

// TestNotice is a notification received by the built-in "模拟 VPN".
type TestNotice struct {
	ID         uint   `gorm:"primaryKey"`
	RunID      uint   `gorm:"not null;index"`
	OutTradeNo string `gorm:"size:64"`
	TradeNo    string `gorm:"size:64"`
	Amount     int64
	Status     string `gorm:"size:16"`
	Code       string `gorm:"size:32"` // our reply: SUCCESS | REJECTED
	Detail     string `gorm:"size:512"`
	CreatedAt  time.Time
}

// Expired reports whether an unpaid order is past its expiry.
func (o *Order) Expired(now time.Time) bool {
	return o.Status != OrderPaid && now.Unix() > o.ExpireAt
}

// VPNStatus is the status reported to the VPN system.
func (o *Order) VPNStatus(now time.Time) string {
	switch {
	case o.Status == OrderPaid:
		return "PAID"
	case o.Expired(now):
		return "CLOSED"
	}
	return "PENDING"
}

// Attempt is one upstream order created for an Order.
type Attempt struct {
	ID                 uint   `gorm:"primaryKey"`
	OrderID            uint   `gorm:"not null;index"`
	UpstreamOutTradeNo string `gorm:"size:40;not null;uniqueIndex"` // our out_trade_no at 信联pay
	ChannelID          uint   `gorm:"not null"`
	PayType            string `gorm:"size:32;not null"`
	Device             string `gorm:"size:16"`
	Money              string `gorm:"size:16;not null"` // yuan, as submitted
	UpstreamTradeNo    string `gorm:"size:64"`
	RespType           string `gorm:"size:16"`
	Payload            string `gorm:"type:text"`
	Status             string `gorm:"size:16;not null;index"`
	Error              string `gorm:"size:512"`
	PaidAt             *time.Time
	LastQueryAt        *time.Time
	CreatedAt          time.Time `gorm:"index"`
	UpdatedAt          time.Time
}

// OrderLog is an event on an order, shown in the admin panel.
type OrderLog struct {
	ID        uint   `gorm:"primaryKey"`
	OrderID   uint   `gorm:"not null;index"`
	Kind      string `gorm:"size:32;not null"`
	Message   string `gorm:"size:1024"`
	CreatedAt time.Time
}

// Open connects and migrates. driver: sqlite | mysql.
func Open(driver, dsn string) (*gorm.DB, error) {
	var d gorm.Dialector
	switch strings.ToLower(driver) {
	case "", "sqlite", "sqlite3":
		if dsn == "" {
			dsn = "data/paycenter.db"
		}
		path, _, _ := strings.Cut(strings.TrimPrefix(dsn, "file:"), "?")
		if dir := filepath.Dir(path); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
		}
		if !strings.Contains(dsn, "_pragma=") {
			sep := "?"
			if strings.Contains(dsn, "?") {
				sep = "&"
			}
			dsn += sep + "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
		}
		d = sqlite.Open(dsn)
	case "mysql":
		if dsn == "" {
			return nil, errors.New("mysql 需要 DSN，例如 user:pass@tcp(127.0.0.1:3306)/paycenter?charset=utf8mb4&parseTime=true&loc=Local")
		}
		d = mysql.Open(dsn)
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q（可选 sqlite / mysql）", driver)
	}
	lg := logger.New(log.New(os.Stderr, "", log.LstdFlags), logger.Config{SlowThreshold: time.Second,
		LogLevel: logger.Warn, IgnoreRecordNotFoundError: true})
	db, err := gorm.Open(d, &gorm.Config{Logger: lg})
	if err != nil {
		return nil, err
	}
	if db.Dialector.Name() == "sqlite" {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, err
		}
		sqlDB.SetMaxOpenConns(1) // one writer; avoids SQLITE_BUSY
	}
	if err := db.AutoMigrate(&Setting{}, &Channel{}, &Merchant{}, &Order{}, &Attempt{}, &OrderLog{}, &TestRun{}, &TestNotice{}); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// GetSetting returns a setting or "".
func GetSetting(db *gorm.DB, key string) string {
	var s Setting
	if err := db.Where("`key` = ?", key).Take(&s).Error; err != nil {
		return ""
	}
	return s.Value
}

// SetSetting upserts a setting.
func SetSetting(db *gorm.DB, key, value string) error {
	return db.Save(&Setting{Key: key, Value: value}).Error
}

// Log records an order event (best effort).
func Log(db *gorm.DB, orderID uint, kind, msg string) {
	_ = db.Create(&OrderLog{OrderID: orderID, Kind: kind, Message: Trunc(msg, 1000)}).Error
}

// Trunc cuts s to at most n characters (not bytes).
func Trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
