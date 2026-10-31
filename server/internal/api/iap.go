package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"vpnplatform/server/internal/store"
)

// Apple in-app purchase (StoreKit) verification. On the iOS App Review face
// the app buys plans through Apple IAP, then POSTs the receipt here; we verify
// it with Apple and grant the plan. The Apple transaction id is the
// idempotency key, so a re-verify or "restore purchases" grants only once.

// iapVerifier checks an App Store receipt. Swappable so tests don't call Apple.
type iapVerifier interface {
	Verify(ctx context.Context, receipt string) (*iapResult, error)
}

type iapResult struct {
	BundleID string
	Items    []iapItem // paid transactions in the receipt
}

type iapItem struct {
	ProductID     string
	TransactionID string // Apple's globally-unique transaction id
}

func (s *Server) handleIAPVerify(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		PlanID  string `json:"plan_id"`
		Receipt string `json:"receipt"` // base64 App Store receipt (StoreKit)
	}
	if !decode(w, r, &req) {
		return
	}
	if s.iap == nil {
		writeErr(w, http.StatusBadRequest, "iap_not_configured", "App 内购买未配置")
		return
	}
	if u.Linked() {
		writeErr(w, http.StatusConflict, "is_member",
			"本机正在使用 "+rechargeCode(u.PrimaryID)+" 的订阅，无法单独购买")
		return
	}
	cat := s.cat()
	plan := cat.Plan(strings.TrimSpace(req.PlanID))
	if plan == nil || plan.Hidden || plan.ID == cat.FreePlan {
		writeErr(w, http.StatusBadRequest, "bad_plan", "套餐不存在")
		return
	}
	if strings.TrimSpace(req.Receipt) == "" {
		writeErr(w, http.StatusBadRequest, "bad_receipt", "缺少购买凭证")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := s.iap.Verify(ctx, req.Receipt)
	if err != nil {
		slog.Warn("iap verify failed", "user", u.ID, "plan", plan.ID, "err", err)
		writeErr(w, http.StatusBadGateway, "verify_failed", "购买凭证校验失败，请稍后重试")
		return
	}
	if a := s.cfg.Payments.AppleIAP; a != nil && a.BundleID != "" && res.BundleID != a.BundleID {
		slog.Warn("iap bundle mismatch", "got", res.BundleID, "want", a.BundleID)
		writeErr(w, http.StatusBadRequest, "bad_receipt", "购买凭证无效")
		return
	}
	want := plan.IAPProductID()
	var txn string
	for _, it := range res.Items {
		if it.ProductID == want {
			txn = it.TransactionID // last match wins (latest transaction)
		}
	}
	if txn == "" {
		writeErr(w, http.StatusBadRequest, "no_matching_purchase", "未找到该套餐的购买记录")
		return
	}
	// The Apple transaction id is the idempotency key.
	orderID := "AIAP" + sanitizeTxn(txn)
	o, _, err := s.store.EnsureOrder(ctx, orderID, u.ID, plan.ID, plan.PriceCents, iapMethod)
	if err != nil {
		s.internal(w, err)
		return
	}
	if o.UserID != u.ID {
		// This transaction was already redeemed by another account.
		writeErr(w, http.StatusConflict, "already_redeemed", "该购买已被其它账号使用")
		return
	}
	granted, err := s.store.MarkPaid(ctx, orderID, txn, plan.DurationDays, time.Now())
	if err != nil {
		s.internal(w, err)
		return
	}
	if granted {
		slog.Info("iap purchase granted", "user", u.ID, "plan", plan.ID, "txn", txn)
		s.trackServer(u.ID, "purchase_paid")
	}
	fresh, _ := s.store.UserByID(ctx, u.ID)
	if fresh == nil {
		fresh = u
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "granted": granted, "user": s.viewOf(ctx, fresh)})
}

func sanitizeTxn(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '_'
	}, s)
}

// ---- Apple verifyReceipt ----

const (
	appleVerifyProd    = "https://buy.itunes.apple.com/verifyReceipt"
	appleVerifySandbox = "https://sandbox.itunes.apple.com/verifyReceipt"
)

type appleVerifier struct {
	secret   string
	bundleID string
	prodURL  string
	sandURL  string
	http     *http.Client
}

func newAppleVerifier(secret, bundleID string) *appleVerifier {
	return &appleVerifier{secret: secret, bundleID: bundleID, prodURL: appleVerifyProd, sandURL: appleVerifySandbox,
		http: &http.Client{Timeout: 12 * time.Second}}
}

// appleReceipt is the subset of the verifyReceipt response we use.
type appleReceipt struct {
	Status  int `json:"status"`
	Receipt struct {
		BundleID string          `json:"bundle_id"`
		InApp    []appleInAppRow `json:"in_app"`
	} `json:"receipt"`
	LatestReceiptInfo []appleInAppRow `json:"latest_receipt_info"`
}

type appleInAppRow struct {
	ProductID     string `json:"product_id"`
	TransactionID string `json:"transaction_id"`
}

func (v *appleVerifier) Verify(ctx context.Context, receipt string) (*iapResult, error) {
	rec, err := v.post(ctx, v.prodURL, receipt)
	if err != nil {
		return nil, err
	}
	// 21007: a sandbox receipt sent to production (TestFlight / App Review).
	if rec.Status == 21007 {
		if rec, err = v.post(ctx, v.sandURL, receipt); err != nil {
			return nil, err
		}
	}
	if rec.Status != 0 {
		return nil, fmt.Errorf("apple verifyReceipt status %d", rec.Status)
	}
	out := &iapResult{BundleID: rec.Receipt.BundleID}
	for _, row := range append(append([]appleInAppRow(nil), rec.Receipt.InApp...), rec.LatestReceiptInfo...) {
		if row.ProductID != "" && row.TransactionID != "" {
			out.Items = append(out.Items, iapItem{ProductID: row.ProductID, TransactionID: row.TransactionID})
		}
	}
	return out, nil
}

func (v *appleVerifier) post(ctx context.Context, url, receipt string) (*appleReceipt, error) {
	body, _ := json.Marshal(map[string]any{
		"receipt-data": receipt, "password": v.secret, "exclude-old-transactions": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("apple verifyReceipt HTTP " + resp.Status)
	}
	var rec appleReceipt
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}
