// Package xinlian is a client for the 信联pay open API (https://c02.z-cn.top/doc):
// form-encoded POST requests signed with MD5 or RSA-SHA256, JSON replies
// {"code":0,"msg":"success","data":{...}}, and MD5-signed form callbacks.
package xinlian

import (
	"context"
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Trade status values.
const (
	TradeWait    = "WAIT_BUYER_PAY"
	TradeSuccess = "TRADE_SUCCESS"
	TradeClosed  = "TRADE_CLOSED"
	TradeRefund  = "REFUND"
)

// CodeNotFound is returned for missing orders and for other merchants' orders.
const CodeNotFound = 4040

// Client calls the API as one merchant (pid).
type Client struct {
	BaseURL  string // e.g. https://c02.z-cn.top
	PID      string
	SignType string // MD5 | RSA
	MD5Key   string // always needed: callbacks are MD5-signed
	RSAKey   *rsa.PrivateKey
	// SignWithType keeps sign_type in the string to sign. The doc's rule
	// removes it, while its worked example keeps it; off by default.
	SignWithType bool
	// Referer is sent as Referer / Origin: the platform only accepts
	// requests from domains authorized for the merchant (error 4484).
	Referer string
	HTTP    *http.Client
}

// Config builds a Client.
type Config struct {
	BaseURL, PID, SignType, MD5Key, RSAPrivateKey string
	SignWithType                                  bool
	Referer                                       string // e.g. https://pay.example.com
}

func New(c Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if u, err := url.Parse(base); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("接口地址必须是 http(s) 地址")
	}
	if strings.TrimSpace(c.PID) == "" {
		return nil, errors.New("商户号 pid 不能为空")
	}
	if strings.TrimSpace(c.MD5Key) == "" {
		return nil, errors.New("MD5 密钥不能为空（异步回调用它验签）")
	}
	cl := &Client{BaseURL: base, PID: strings.TrimSpace(c.PID), SignType: strings.ToUpper(strings.TrimSpace(c.SignType)),
		MD5Key: strings.TrimSpace(c.MD5Key), SignWithType: c.SignWithType, Referer: strings.TrimRight(strings.TrimSpace(c.Referer), "/"),
		HTTP: &http.Client{Timeout: 10 * time.Second}}
	switch cl.SignType {
	case "", "MD5":
		cl.SignType = "MD5"
	case "RSA":
		k, err := ParsePrivateKey(c.RSAPrivateKey)
		if err != nil {
			return nil, err
		}
		cl.RSAKey = k
	default:
		return nil, errors.New("签名方式只能是 MD5 或 RSA")
	}
	return cl, nil
}

// ParsePrivateKey accepts PEM or bare base64, PKCS#8 or PKCS#1.
func ParsePrivateKey(s string) (*rsa.PrivateKey, error) {
	s = strings.TrimSpace(s)
	var der []byte
	if b, _ := pem.Decode([]byte(s)); b != nil {
		der = b.Bytes
	} else {
		d, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return nil, errors.New("RSA 私钥格式错误（需 PEM 或 Base64）")
		}
		der = d
	}
	if k, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if rk, ok := k.(*rsa.PrivateKey); ok {
			return rk, nil
		}
		return nil, errors.New("不是 RSA 私钥")
	}
	k, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, errors.New("RSA 私钥解析失败")
	}
	return k, nil
}

// SignString builds the string to sign: drop sign, sign_type (unless
// withType) and empty values; sort keys; "k=v" joined by "&"; no URL escaping.
func SignString(p map[string]string, withType bool) string {
	keys := make([]string, 0, len(p))
	for k, v := range p {
		if k == "sign" || (k == "sign_type" && !withType) || v == "" {
			continue
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

func md5Hex(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

func (c *Client) sign(p map[string]string) (string, error) {
	str := SignString(p, c.SignWithType)
	if c.SignType == "RSA" {
		h := sha256.Sum256([]byte(str))
		sig, err := rsa.SignPKCS1v15(rand.Reader, c.RSAKey, crypto.SHA256, h[:])
		if err != nil {
			return "", err
		}
		return base64.StdEncoding.EncodeToString(sig), nil
	}
	return md5Hex(str + c.MD5Key), nil
}

// VerifyNotify checks a callback's pid and MD5 signature. Both readings of
// the doc (sign_type in or out of the signed string) are accepted.
func (c *Client) VerifyNotify(form url.Values) error {
	p := map[string]string{}
	for k := range form {
		p[k] = form.Get(k)
	}
	if p["pid"] != c.PID {
		return errors.New("pid mismatch")
	}
	got := strings.ToLower(p["sign"])
	if got == "" {
		return errors.New("missing sign")
	}
	for _, withType := range []bool{false, true} {
		want := md5Hex(SignString(p, withType) + c.MD5Key)
		if subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1 {
			return nil
		}
	}
	return errors.New("bad sign")
}

// APIError is a reply with code != 0.
type APIError struct {
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("信联pay %d %s", e.Code, e.Msg)
	switch e.Code {
	case CodeRefererDenied:
		s += "（请在信联pay 商户后台把本支付中心的域名加入授权域名）"
	case CodeChannelMissing:
		s += "（这种支付方式在信联pay 未开通或已停用：请在商户后台开通，或核对通道编码）"
	}
	return s
}

// CodeRefererDenied: the request's domain is not authorized for the merchant.
const CodeRefererDenied = 4484

// CodeChannelMissing: no active upstream channel for this pay type.
const CodeChannelMissing = 4040

// CodeBelowMinimum: amount under the merchant's minimum ("订单金额低于最低限额 100 分").
const CodeBelowMinimum = 4002

// IsCode reports whether err is an APIError with this code.
func IsCode(err error, code int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Code == code
}

func nonce() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Call POSTs biz params plus the public signing params and returns data.
func (c *Client) Call(ctx context.Context, path string, biz map[string]string) (json.RawMessage, error) {
	p := map[string]string{}
	for k, v := range biz {
		if v != "" {
			p[k] = v
		}
	}
	p["pid"] = c.PID
	p["sign_type"] = c.SignType
	p["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
	p["nonce"] = nonce()
	sig, err := c.sign(p)
	if err != nil {
		return nil, err
	}
	p["sign"] = sig
	form := url.Values{}
	for k, v := range p {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.Referer != "" {
		req.Header.Set("Referer", c.Referer+"/")
		req.Header.Set("Origin", c.Referer)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("信联pay %s: %w", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var r struct {
		Code json.RawMessage `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || len(r.Code) == 0 {
		return nil, fmt.Errorf("信联pay %s: HTTP %d, 非 JSON 应答: %.200s", path, res.StatusCode, body)
	}
	code, err := strconv.Atoi(strings.Trim(string(r.Code), `"`))
	if err != nil {
		return nil, fmt.Errorf("信联pay %s: bad code %s", path, r.Code)
	}
	if code != 0 {
		return nil, &APIError{Code: code, Msg: r.Msg}
	}
	return r.Data, nil
}

// SubmitRequest is /api/pay/submit's business params.
type SubmitRequest struct {
	Type, OutTradeNo, Money, Name, NotifyURL, ReturnURL, Device string
}

// SubmitResult is /api/pay/submit's data.
type SubmitResult struct {
	Type       string          `json:"type"`
	TradeNo    string          `json:"trade_no"`
	OutTradeNo string          `json:"out_trade_no"` // the doc says this carries the platform order no
	Money      string          `json:"money"`
	Payload    json.RawMessage `json:"payload"`
}

// PlatformTradeNo is the platform's order number from the reply.
func (r *SubmitResult) PlatformTradeNo() string {
	if r.TradeNo != "" {
		return r.TradeNo
	}
	return r.OutTradeNo
}

func (c *Client) Submit(ctx context.Context, s SubmitRequest) (*SubmitResult, error) {
	data, err := c.Call(ctx, "/api/pay/submit", map[string]string{
		"type": s.Type, "out_trade_no": s.OutTradeNo, "money": s.Money, "name": s.Name,
		"notify_url": s.NotifyURL, "return_url": s.ReturnURL, "device": s.Device,
	})
	if err != nil {
		return nil, err
	}
	var r SubmitResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("信联pay submit: bad data: %w", err)
	}
	return &r, nil
}

// QueryResult is /api/pay/query's data.
type QueryResult struct {
	TradeNo     string `json:"trade_no"`
	OutTradeNo  string `json:"out_trade_no"`
	Money       string `json:"money"`
	TradeStatus string `json:"trade_status"`
}

// Query looks an order up by our out_trade_no. A missing order is an
// APIError with CodeNotFound.
func (c *Client) Query(ctx context.Context, outTradeNo string) (*QueryResult, error) {
	data, err := c.Call(ctx, "/api/pay/query", map[string]string{"out_trade_no": outTradeNo})
	if err != nil {
		return nil, err
	}
	var r QueryResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("信联pay query: bad data: %w", err)
	}
	return &r, nil
}

// Close closes a pending order.
func (c *Client) Close(ctx context.Context, outTradeNo string) error {
	_, err := c.Call(ctx, "/api/pay/close", map[string]string{"out_trade_no": outTradeNo})
	return err
}

// MerchantInfo calls /api/merchant/info (used to test the credentials).
func (c *Client) MerchantInfo(ctx context.Context) (json.RawMessage, error) {
	return c.Call(ctx, "/api/merchant/info", nil)
}

// YuanToCents parses "10", "10.5", "10.00" without floating point.
func YuanToCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") || strings.HasPrefix(s, "+") {
		return 0, errors.New("bad amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 {
		if strings.Trim(frac[2:], "0") != "" {
			return 0, errors.New("bad amount")
		}
		frac = frac[:2]
	}
	frac += strings.Repeat("0", 2-len(frac))
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, errors.New("bad amount")
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errors.New("bad amount")
	}
	return w*100 + f, nil
}

// CentsToYuan formats 2500 as "25.00".
func CentsToYuan(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }
