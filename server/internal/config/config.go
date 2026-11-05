// Package config loads JSON configuration for the API server and node agent.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// API is the API server configuration. Static settings (listen address,
// secrets, payments) live only in the JSON file; the embedded Catalog is the
// admin-editable part (see catalog.go).
type API struct {
	Listen        string `json:"listen"`
	Database      string `json:"database"`
	JWTSecret     string `json:"jwt_secret"` // or env VPN_JWT_SECRET
	TokenTTLHours int    `json:"token_ttl_hours"`
	AllowRegister bool   `json:"allow_register"`
	// TrustProxyHeader: use X-Forwarded-For for client IP (only behind your own reverse proxy).
	TrustProxyHeader bool `json:"trust_proxy_header"`
	// TrustCloudflare: the API domain is proxied by Cloudflare. The client IP
	// is then read from CF-Connecting-IP on connections from Cloudflare's
	// address ranges (never from anyone else).
	TrustCloudflare bool `json:"trust_cloudflare"`
	// PublicURL is the externally reachable base URL of this API (payment callbacks, invite links).
	PublicURL string          `json:"public_url"`
	Payments  Payments        `json:"payments"`
	Admin     AdminConfig     `json:"admin"`
	Provision ProvisionConfig `json:"provision"`
	// DownloadDir holds files served at /dl/<name>, e.g. app.apk for the invite page.
	DownloadDir string `json:"download_dir"`
	// TLS, if Domain is set, makes the API terminate HTTPS itself on :443 with
	// an automatic Let's Encrypt certificate (and redirect :80). Used by
	// cloned deployments that have no nginx in front.
	TLS TLSConfig `json:"tls"`

	Catalog // seed for the database copy; JSON keys stay flat for compatibility
}

// ProvisionConfig enables one-click node installation over SSH from the admin
// panel. The API uploads a vpn-node binary matching the target's architecture.
type ProvisionConfig struct {
	// BinaryAmd64 / BinaryArm64 are filesystem paths to linux vpn-node binaries.
	// BinaryAmd64 defaults to the API's own installed node binary, so the common
	// single-arch (x86_64) case needs no extra configuration.
	BinaryAmd64 string `json:"binary_amd64"`
	BinaryArm64 string `json:"binary_arm64"`
	// RealityTarget is the camouflage site new nodes impersonate, e.g. "www.apple.com:443".
	RealityTarget string `json:"reality_target"`
}

// TLSConfig enables built-in HTTPS with automatic certificates.
type TLSConfig struct {
	Domain string `json:"domain"` // e.g. vpn.example.com ("" = plain HTTP on Listen)
	// Aliases are more domains served exactly like Domain (own certificate
	// each). Used when the API moves to a new domain: installed apps have the
	// old one built in and keep working (and receive the update) through it.
	Aliases  []string `json:"aliases"`
	Email    string   `json:"email"`     // optional Let's Encrypt contact
	CacheDir string   `json:"cache_dir"` // certificate cache, default <db dir>/acme
	// Passthrough: host:port of a VPN node on the same server. :443 is then
	// shared: connections whose TLS SNI is Domain go to the API, all others
	// are relayed to the node with a PROXY protocol header (the node must
	// set accept_proxy_protocol).
	Passthrough string `json:"passthrough"`
}

// Domains is Domain followed by its Aliases (empty without built-in TLS).
func (t TLSConfig) Domains() []string {
	if t.Domain == "" {
		return nil
	}
	return append([]string{strings.ToLower(t.Domain)}, t.Aliases...)
}

// AdminConfig secures the web admin panel. Password can come from env VPN_ADMIN_PASSWORD.
type AdminConfig struct {
	Password string `json:"password"`
}

type Payments struct {
	Alipay  *AlipayConfig  `json:"alipay"`
	Epay    *EpayConfig    `json:"epay"`
	Cashier *CashierConfig `json:"cashier"`
	// AppleIAP verifies iOS in-app purchase receipts (StoreKit), needed for
	// the App Store review face.
	AppleIAP *AppleIAPConfig `json:"apple_iap"`
	// GooglePlay verifies Android in-app purchases (Google Play Billing),
	// needed for the Google Play build channel.
	GooglePlay *GooglePlayConfig `json:"google_play"`
	// Mock enables a fake payment page for development. NEVER enable in production.
	Mock bool `json:"mock"`
}

// AppleIAPConfig verifies App Store in-app purchases.
type AppleIAPConfig struct {
	// SharedSecret: App Store Connect → 你的 App → App 信息 → App 专用共享密钥
	// (used by the legacy verifyReceipt API). Or env VPN_APPLE_SHARED_SECRET.
	SharedSecret string `json:"shared_secret"`
	// BundleID guards against receipts from another app (e.g. com.huanyou.vpn).
	BundleID string `json:"bundle_id"`
}

// GooglePlayConfig verifies Google Play in-app purchases with the Play
// Developer API. Create a Google Cloud service account, grant it access in
// Play Console (财务 → 订单管理 / 用户和权限, with "查看财务数据、订单和取消调查回复"
// and "管理订单和订阅"), download its JSON key.
type GooglePlayConfig struct {
	// PackageName is the Android applicationId, e.g. com.vpnplatform.vpn_app.
	// Guards against purchase tokens from another app.
	PackageName string `json:"package_name"`
	// ServiceAccountFile is the path to the service-account JSON key file. The
	// key can also be supplied inline as the env var VPN_GOOGLE_SA_JSON (the
	// whole JSON), which takes precedence.
	ServiceAccountFile string `json:"service_account_file"`
}

// AlipayConfig: official Alipay open platform, 手机网站支付 (alipay.trade.wap.pay).
type AlipayConfig struct {
	AppID string `json:"app_id"`
	// PEM or bare base64 (as shown in the Alipay console).
	PrivateKeyFile      string `json:"private_key_file"`
	AlipayPublicKeyFile string `json:"alipay_public_key_file"`
	// Gateway: https://openapi.alipay.com/gateway.do (sandbox: https://openapi-sandbox.dl.alipaydev.com/gateway.do)
	Gateway string `json:"gateway"`
}

// CashierConfig: our hosted-checkout protocol "收银台 v1" (docs/payment-api.md),
// AES-256-GCM encrypted + HMAC-SHA256 signed in both directions.
type CashierConfig struct {
	APIURL     string `json:"api_url"`
	MerchantID string `json:"merchant_id"`
	SignKey    string `json:"sign_key"` // or env VPN_CASHIER_SIGN_KEY (hex/base64, ≥16 bytes)
	EncKey     string `json:"enc_key"`  // or env VPN_CASHIER_ENC_KEY (hex/base64, 32 bytes)
	// Channels shown in the app: [{"id":"alipay","name":"支付宝"}, …]
	Channels []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channels"`
}

// EpayConfig: "易支付" aggregator protocol (submit.php + MD5 sign), supports alipay / wxpay / qqpay.
type EpayConfig struct {
	APIURL string   `json:"api_url"`
	PID    string   `json:"pid"`
	Key    string   `json:"key"` // or env VPN_EPAY_KEY
	Types  []string `json:"types"`
}

func LoadAPI(path string) (*API, error) {
	c := &API{Listen: ":8080", Database: "data/vpn.db", TokenTTLHours: 24 * 30, AllowRegister: true}
	if err := loadJSON(path, c); err != nil {
		return nil, err
	}
	if v := os.Getenv("VPN_JWT_SECRET"); v != "" {
		c.JWTSecret = v
	}
	if v := os.Getenv("VPN_ADMIN_PASSWORD"); v != "" {
		c.Admin.Password = v
	}
	if e := c.Payments.Epay; e != nil {
		if v := os.Getenv("VPN_EPAY_KEY"); v != "" {
			e.Key = v
		}
		if e.APIURL == "" || e.PID == "" || e.Key == "" || len(e.Types) == 0 {
			return nil, errors.New("payments.epay: api_url, pid, key and types are required")
		}
	}
	if a := c.Payments.Alipay; a != nil {
		if a.AppID == "" || a.PrivateKeyFile == "" || a.AlipayPublicKeyFile == "" {
			return nil, errors.New("payments.alipay: app_id, private_key_file and alipay_public_key_file are required")
		}
		if a.Gateway == "" {
			a.Gateway = "https://openapi.alipay.com/gateway.do"
		}
	}
	if c.DownloadDir == "" {
		c.DownloadDir = "/var/lib/vpn/dl"
	}
	if c.TLS.Domain != "" && c.TLS.CacheDir == "" {
		c.TLS.CacheDir = filepath.Join(filepath.Dir(c.Database), "acme")
	}
	aliases := c.TLS.Aliases[:0]
	for _, a := range c.TLS.Aliases {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" && a != strings.ToLower(c.TLS.Domain) && !slices.Contains(aliases, a) {
			aliases = append(aliases, a)
		}
	}
	c.TLS.Aliases = aliases
	if c.Provision.BinaryAmd64 == "" {
		c.Provision.BinaryAmd64 = "/usr/local/bin/vpn-node"
	}
	if c.Provision.RealityTarget == "" {
		c.Provision.RealityTarget = "www.apple.com:443"
	}
	if k := c.Payments.Cashier; k != nil {
		if v := os.Getenv("VPN_CASHIER_SIGN_KEY"); v != "" {
			k.SignKey = v
		}
		if v := os.Getenv("VPN_CASHIER_ENC_KEY"); v != "" {
			k.EncKey = v
		}
		if k.APIURL == "" || k.MerchantID == "" || k.SignKey == "" || k.EncKey == "" {
			return nil, errors.New("payments.cashier: api_url, merchant_id, sign_key and enc_key are required")
		}
	}
	if g := c.Payments.GooglePlay; g != nil {
		if g.PackageName == "" {
			return nil, errors.New("payments.google_play: package_name is required")
		}
		if g.ServiceAccountFile == "" && os.Getenv("VPN_GOOGLE_SA_JSON") == "" {
			return nil, errors.New("payments.google_play: service_account_file (or env VPN_GOOGLE_SA_JSON) is required")
		}
	}
	hasPayments := c.Payments.Alipay != nil || c.Payments.Epay != nil || c.Payments.Cashier != nil || c.Payments.Mock
	if hasPayments && !strings.HasPrefix(c.PublicURL, "http") {
		return nil, errors.New("public_url is required when payments are configured")
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	if err := c.Catalog.Normalize(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func sortSlice[T any](s []T, less func(a, b T) bool) {
	slices.SortStableFunc(s, func(a, b T) int {
		switch {
		case less(a, b):
			return -1
		case less(b, a):
			return 1
		}
		return 0
	})
}

// NodeAgent is the configuration of a proxy node.
type NodeAgent struct {
	APIURL              string `json:"api_url"`
	Token               string `json:"token"` // same value as Node.Token on the API side
	SyncIntervalSeconds int    `json:"sync_interval_seconds"`
	LogLevel            string `json:"log_level"`

	Listen     string `json:"listen"`
	VisionPort int    `json:"vision_port"` // public port, keep 443
	// XHTTPPath enables the XHTTP fallback transport on the same public port ("" = off).
	XHTTPPath string `json:"xhttp_path"`
	// XHTTPInternal is where the internal XHTTP inbound listens (loopback or unix socket).
	XHTTPInternal string `json:"xhttp_internal"`
	// AcceptProxyProtocol: set when a front proxy (e.g. nginx stream with
	// ssl_preread SNI routing) forwards to the node with PROXY protocol, so
	// the node sees real client IPs.
	AcceptProxyProtocol bool `json:"accept_proxy_protocol"`

	Reality struct {
		PrivateKey string   `json:"private_key"`
		ShortIDs   []string `json:"short_ids"`
		// Target: a real, foreign TLS 1.3 + HTTP/2 site the node impersonates,
		// e.g. "www.microsoft.com:443". Unauthenticated probes are forwarded here.
		Target      string   `json:"target"`
		ServerNames []string `json:"server_names"`
	} `json:"reality"`
}

func LoadNode(path string) (*NodeAgent, error) {
	c := &NodeAgent{SyncIntervalSeconds: 30, LogLevel: "warning", Listen: "0.0.0.0", VisionPort: 443,
		XHTTPInternal: "127.0.0.1:2001"}
	if err := loadJSON(path, c); err != nil {
		return nil, err
	}
	if v := os.Getenv("VPN_NODE_TOKEN"); v != "" {
		c.Token = v
	}
	if v := os.Getenv("VPN_REALITY_PRIVATE_KEY"); v != "" {
		c.Reality.PrivateKey = v
	}
	switch {
	case c.APIURL == "" || c.Token == "":
		return nil, errors.New("api_url and token are required")
	case c.Reality.PrivateKey == "" || c.Reality.Target == "" || len(c.Reality.ServerNames) == 0:
		return nil, errors.New("reality.private_key, reality.target and reality.server_names are required")
	case len(c.Reality.ShortIDs) == 0:
		return nil, errors.New("reality.short_ids must not be empty")
	case c.XHTTPPath != "" && !strings.HasPrefix(c.XHTTPPath, "/"):
		return nil, errors.New("xhttp_path must start with /")
	}
	if !strings.HasPrefix(c.APIURL, "https://") && !strings.HasPrefix(c.APIURL, "http://127.0.0.1") && !strings.HasPrefix(c.APIURL, "http://localhost") {
		// Not refused (a running node must keep starting), but the token and
		// every user's UUID travel to this URL in the clear.
		slog.Warn("api_url is not https: the node token and user ids are sent unencrypted", "api_url", c.APIURL)
	}
	if c.SyncIntervalSeconds <= 0 {
		c.SyncIntervalSeconds = 30
	}
	c.SyncIntervalSeconds = min(max(c.SyncIntervalSeconds, 5), 300)
	// Privacy: Xray's info / debug messages name the destinations users
	// visit ("dialing to …"); only warnings and errors are ever logged.
	switch c.LogLevel {
	case "warning", "error", "none":
	default:
		c.LogLevel = "warning"
	}
	return c, nil
}

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
