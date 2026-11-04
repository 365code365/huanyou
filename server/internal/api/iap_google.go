package api

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/store"
)

// Google Play in-app purchase (Google Play Billing) verification. On the
// Google Play build channel the app buys a plan through Play Billing, then
// POSTs the purchase here; we verify it with the Play Developer API and grant
// the plan. Google's orderId is the idempotency key, so a re-verify or a
// re-delivered purchase grants only once.
//
// This is the Android counterpart of iap.go (Apple). No web order is created:
// handleCreateOrder refuses the Google Play face, as it does the iOS face.

// gplayVerifier checks a Play purchase token. Swappable so tests don't call Google.
type gplayVerifier interface {
	// Verify looks up a one-time product purchase by its product id and token.
	Verify(ctx context.Context, productID, token string) (*gplayPurchase, error)
}

type gplayPurchase struct {
	OrderID       string // Google order id, e.g. GPA.1234-5678-9012-34567 (idempotency key)
	PurchaseState int    // 0 = purchased, 1 = cancelled, 2 = pending
	PurchasedAt   int64  // unix ms
}

// handleGPlayVerify: POST /api/v1/iap/google/verify
//
// Body is either the raw purchase data the Play Billing library returns
// ({"data": "<originalJson>"}) or the parsed fields ({"product_id","token"}).
// The product id maps to a plan; we verify the token with Google and grant.
func (s *Server) handleGPlayVerify(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Data      string `json:"data"`       // Play Billing originalJson (preferred)
		ProductID string `json:"product_id"` // or the parsed fields
		Token     string `json:"token"`
	}
	if !decode(w, r, &req) {
		return
	}
	if s.gplay == nil {
		writeErr(w, http.StatusBadRequest, "iap_not_configured", "App 内购买未配置")
		return
	}
	productID, token := strings.TrimSpace(req.ProductID), strings.TrimSpace(req.Token)
	// Prefer the fields parsed from the purchase originalJson when present; the
	// data may also be a bare token in some Play Billing versions, in which
	// case we keep the explicit product_id / token fields.
	if d := strings.TrimSpace(req.Data); strings.HasPrefix(d, "{") {
		var pj struct {
			ProductID     string `json:"productId"`
			PurchaseToken string `json:"purchaseToken"`
		}
		if json.Unmarshal([]byte(d), &pj) == nil {
			if pj.ProductID != "" {
				productID = pj.ProductID
			}
			if pj.PurchaseToken != "" {
				token = pj.PurchaseToken
			}
		}
	}
	if productID == "" || token == "" {
		writeErr(w, http.StatusBadRequest, "bad_receipt", "缺少购买凭证")
		return
	}
	if u.Linked() {
		writeErr(w, http.StatusConflict, "is_member",
			"本机正在使用 "+rechargeCode(u.PrimaryID)+" 的订阅，无法单独购买")
		return
	}
	cat := s.cat()
	// Map the Google Play product id back to a plan.
	var plan *config.Plan
	for i := range cat.Plans {
		p := &cat.Plans[i]
		if p.ID != cat.FreePlan && p.GooglePlayProductID() == productID {
			plan = p
			break
		}
	}
	if plan == nil || plan.Hidden {
		writeErr(w, http.StatusBadRequest, "bad_plan", "套餐不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	pur, err := s.gplay.Verify(ctx, productID, token)
	if err != nil {
		slog.Warn("gplay verify failed", "user", u.ID, "plan", plan.ID, "err", err)
		writeErr(w, http.StatusBadGateway, "verify_failed", "购买凭证校验失败，请稍后重试")
		return
	}
	if pur.PurchaseState != 0 {
		// 1 = cancelled / refunded, 2 = pending (not yet paid).
		writeErr(w, http.StatusBadRequest, "no_matching_purchase", "未找到该套餐的购买记录")
		return
	}
	// Google's order id is the idempotency key; fall back to the purchase
	// token if a purchase somehow carries none.
	idem := pur.OrderID
	if idem == "" {
		idem = token
	}
	orderID := "GIAP" + sanitizeTxn(idem)
	o, _, err := s.store.EnsureOrder(ctx, orderID, u.ID, plan.ID, plan.PriceCents, gplayMethod)
	if err != nil {
		s.internal(w, err)
		return
	}
	granted := false
	switch {
	case o.UserID != u.ID:
		writeErr(w, http.StatusConflict, "already_redeemed", "该购买已被其它账号使用")
		return
	case o.Status != "paid":
		ok, err := s.store.MarkPaid(ctx, orderID, idem, plan.DurationDays, time.Now())
		if err != nil {
			s.internal(w, err)
			return
		}
		if ok {
			granted = true
			slog.Info("gplay purchase granted", "user", u.ID, "plan", plan.ID, "order", pur.OrderID)
			s.trackServer(u.ID, "purchase_paid")
		}
	}
	fresh, _ := s.store.UserByID(ctx, u.ID)
	if fresh == nil {
		fresh = u
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "granted": granted, "user": s.viewOf(ctx, fresh)})
}

// ---- Play Developer API ----

const (
	googleTokenURL       = "https://oauth2.googleapis.com/token"
	googlePublisherScope = "https://www.googleapis.com/auth/androidpublisher"
	androidPublisherAPI  = "https://androidpublisher.googleapis.com/androidpublisher/v3"
)

// googleVerifier calls the Play Developer API with a service-account JWT
// (RS256) exchanged for an OAuth2 access token. Self-contained (standard
// library + the JWT package already used for sessions), so it adds no module
// dependency.
type googleVerifier struct {
	packageName string
	clientEmail string
	key         *rsa.PrivateKey
	http        *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
}

// serviceAccount is the subset of a Google service-account JSON key we need.
type serviceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

func newGoogleVerifier(saJSON []byte, packageName string) (*googleVerifier, error) {
	var sa serviceAccount
	if err := json.Unmarshal(saJSON, &sa); err != nil {
		return nil, fmt.Errorf("parse service account json: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("service account json missing client_email / private_key")
	}
	key, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, err
	}
	return &googleVerifier{
		packageName: packageName,
		clientEmail: sa.ClientEmail,
		key:         key,
		http:        &http.Client{Timeout: 12 * time.Second},
	}, nil
}

func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("service account private_key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	anyKey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	k, ok := anyKey.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("service account private_key is not RSA")
	}
	return k, nil
}

// accessToken returns a cached OAuth2 token, minting a new one when it is
// within a minute of expiry.
func (v *googleVerifier) accessToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token != "" && time.Now().Before(v.expiry.Add(-time.Minute)) {
		return v.token, nil
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   v.clientEmail,
		"scope": googlePublisherScope,
		"aud":   googleTokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	assertion, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(v.key)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("google token endpoint HTTP %d: %s", resp.StatusCode, string(raw))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("google token endpoint returned no access_token")
	}
	v.token = tr.AccessToken
	v.expiry = now.Add(time.Duration(tr.ExpiresIn) * time.Second)
	return v.token, nil
}

func (v *googleVerifier) Verify(ctx context.Context, productID, token string) (*gplayPurchase, error) {
	at, err := v.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/applications/%s/purchases/products/%s/tokens/%s",
		androidPublisherAPI, url.PathEscape(v.packageName), url.PathEscape(productID), url.PathEscape(token))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+at)
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("play developer api HTTP %d: %s", resp.StatusCode, string(raw))
	}
	// https://developers.google.com/android-publisher/api-ref/rest/v3/purchases.products
	var pr struct {
		OrderID            string `json:"orderId"`
		PurchaseState      int    `json:"purchaseState"` // 0 purchased, 1 cancelled, 2 pending
		PurchaseTimeMillis string `json:"purchaseTimeMillis"`
	}
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, err
	}
	at2, _ := strconv.ParseInt(pr.PurchaseTimeMillis, 10, 64)
	return &gplayPurchase{OrderID: pr.OrderID, PurchaseState: pr.PurchaseState, PurchasedAt: at2}, nil
}
