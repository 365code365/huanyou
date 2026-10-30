package payment

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// Alipay implements 支付宝手机网站支付 (alipay.trade.wap.pay) with RSA2 signatures.
// The pay URL opens Alipay's cashier in the browser, which hands off to the
// Alipay app when installed.
type Alipay struct {
	AppID     string
	Gateway   string
	priv      *rsa.PrivateKey
	alipayPub *rsa.PublicKey
}

func NewAlipay(appID, gateway, privateKeyFile, alipayPublicKeyFile string) (*Alipay, error) {
	privPEM, err := readKey(privateKeyFile, "PRIVATE KEY")
	if err != nil {
		return nil, fmt.Errorf("alipay private key: %w", err)
	}
	pubPEM, err := readKey(alipayPublicKeyFile, "PUBLIC KEY")
	if err != nil {
		return nil, fmt.Errorf("alipay public key: %w", err)
	}
	priv, err := parsePrivateKey(privPEM)
	if err != nil {
		return nil, fmt.Errorf("alipay private key: %w", err)
	}
	pub, err := parsePublicKey(pubPEM)
	if err != nil {
		return nil, fmt.Errorf("alipay public key: %w", err)
	}
	return &Alipay{AppID: appID, Gateway: gateway, priv: priv, alipayPub: pub}, nil
}

func (a *Alipay) Name() string      { return "alipay" }
func (a *Alipay) Ack() string       { return "success" }
func (a *Alipay) Methods() []Method { return []Method{{ID: "alipay", Name: "支付宝"}} }

var cst = time.FixedZone("CST", 8*3600)

func (a *Alipay) PayURL(o Order, _ string, notifyURL, returnURL string) (string, error) {
	biz, _ := json.Marshal(map[string]string{
		"out_trade_no": o.ID,
		"total_amount": Yuan(o.AmountCents),
		"subject":      o.Subject,
		"product_code": "QUICK_WAP_WAY",
		"quit_url":     returnURL,
	})
	p := map[string]string{
		"app_id":      a.AppID,
		"method":      "alipay.trade.wap.pay",
		"format":      "JSON",
		"charset":     "utf-8",
		"sign_type":   "RSA2",
		"timestamp":   time.Now().In(cst).Format("2006-01-02 15:04:05"),
		"version":     "1.0",
		"notify_url":  notifyURL,
		"return_url":  returnURL,
		"biz_content": string(biz),
	}
	sig, err := a.sign(signContent(p, "sign"))
	if err != nil {
		return "", err
	}
	q := url.Values{}
	for k, v := range p {
		q.Set(k, v)
	}
	q.Set("sign", sig)
	return a.Gateway + "?" + q.Encode(), nil
}

func (a *Alipay) ParseNotify(r *http.Request) (*Notification, error) {
	p, err := formParams(r)
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(p["sign"])
	if err != nil || len(sig) == 0 {
		return nil, ErrBadSignature
	}
	// Alipay async notifications: sign covers all params except sign and sign_type.
	h := sha256.Sum256([]byte(signContent(p, "sign", "sign_type")))
	if rsa.VerifyPKCS1v15(a.alipayPub, crypto.SHA256, h[:], sig) != nil {
		return nil, ErrBadSignature
	}
	if p["app_id"] != a.AppID {
		return nil, errors.New("alipay: app_id mismatch")
	}
	amount, err := ParseYuan(p["total_amount"])
	if err != nil {
		return nil, err
	}
	st := p["trade_status"]
	return &Notification{
		OrderID: p["out_trade_no"], TradeNo: p["trade_no"], AmountCents: amount,
		Paid: st == "TRADE_SUCCESS" || st == "TRADE_FINISHED",
	}, nil
}

func (a *Alipay) sign(content string) (string, error) {
	h := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.priv, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// signContent joins non-empty params as k=v sorted by key, skipping excluded keys.
func signContent(p map[string]string, exclude ...string) string {
	keys := make([]string, 0, len(p))
outer:
	for k, v := range p {
		if v == "" {
			continue
		}
		for _, e := range exclude {
			if k == e {
				continue outer
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k + "=" + p[k])
	}
	return b.String()
}

// readKey loads a PEM file, or a bare base64 key as copied from the Alipay console.
func readKey(path, kind string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "-----BEGIN") {
		return []byte(s), nil
	}
	return []byte("-----BEGIN " + kind + "-----\n" + s + "\n-----END " + kind + "-----\n"), nil
}

func parsePrivateKey(p []byte) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode(p)
	if blk == nil {
		return nil, errors.New("invalid PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, errors.New("not an RSA key")
	}
	return x509.ParsePKCS1PrivateKey(blk.Bytes)
}

func parsePublicKey(p []byte) (*rsa.PublicKey, error) {
	blk, _ := pem.Decode(p)
	if blk == nil {
		return nil, errors.New("invalid PEM")
	}
	if k, err := x509.ParsePKIXPublicKey(blk.Bytes); err == nil {
		if rk, ok := k.(*rsa.PublicKey); ok {
			return rk, nil
		}
		return nil, errors.New("not an RSA key")
	}
	return x509.ParsePKCS1PublicKey(blk.Bytes)
}
