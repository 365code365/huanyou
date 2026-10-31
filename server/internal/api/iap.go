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
	"sort"
	"strconv"
	"strings"
	"time"

	"vpnplatform/server/internal/config"
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
	Sandbox  bool      // verified by Apple's sandbox (TestFlight, App Review): no money was paid
	Items    []iapItem // transactions in the receipt
}

type iapItem struct {
	ProductID     string
	TransactionID string // Apple's globally-unique transaction id
	PurchasedAt   int64  // unix ms
	Cancelled     bool   // refunded / revoked by Apple: grants nothing
}

// refuseIAPResult answers with an error (and returns true) for receipts that
// must not grant anything here:
//   - from another app (verifyReceipt validates any app's receipt, and
//     product ids are guessable): the bundle id is required;
//   - from the sandbox (free test purchases) unless a build is under App
//     Review: that is the only time this server offers in-app purchase.
//
// The messages stay literal writeErr calls: scripts/i18n-extract.py collects
// them from there for the language packs.
func (s *Server) refuseIAPResult(w http.ResponseWriter, res *iapResult) bool {
	a := s.cfg.Payments.AppleIAP
	if a == nil || a.BundleID == "" || res.BundleID != a.BundleID {
		slog.Warn("iap receipt for another app", "got", res.BundleID)
		writeErr(w, http.StatusBadRequest, "bad_receipt", "购买凭证无效")
		return true
	}
	if res.Sandbox && s.cat().Payment.IOSReviewBuild <= 0 {
		slog.Warn("iap sandbox receipt outside App Review")
		writeErr(w, http.StatusBadRequest, "sandbox_receipt", "测试环境的购买凭证仅在 App 审核期间有效")
		return true
	}
	return false
}

func iapTradeNo(res *iapResult, txn string) string {
	if res.Sandbox {
		return "sandbox:" + txn // marks test purchases in the order list
	}
	return txn
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
	if s.refuseIAPResult(w, res) {
		return
	}
	// A receipt holds every purchase of this Apple ID. Redeem the newest one
	// not yet redeemed (normally the purchase just made; an older one only if
	// its own verify never reached us). Refunded ones grant nothing.
	want := plan.IAPProductID()
	var cands []iapItem
	for _, it := range res.Items {
		if it.ProductID == want && !it.Cancelled {
			cands = append(cands, it)
		}
	}
	if len(cands) == 0 {
		writeErr(w, http.StatusBadRequest, "no_matching_purchase", "未找到该套餐的购买记录")
		return
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].PurchasedAt > cands[j].PurchasedAt })
	granted, ours, other := false, false, false
	for _, it := range cands {
		// The Apple transaction id is the idempotency key.
		orderID := "AIAP" + sanitizeTxn(it.TransactionID)
		o, _, err := s.store.EnsureOrder(ctx, orderID, u.ID, plan.ID, plan.PriceCents, iapMethod)
		if err != nil {
			s.internal(w, err)
			return
		}
		switch {
		case o.UserID != u.ID:
			other = true // redeemed by another account
			continue
		case o.Status == "paid":
			ours = true
			continue
		}
		ok, err := s.store.MarkPaid(ctx, orderID, iapTradeNo(res, it.TransactionID), plan.DurationDays, time.Now())
		if err != nil {
			s.internal(w, err)
			return
		}
		if ok {
			granted = true
			slog.Info("iap purchase granted", "user", u.ID, "plan", plan.ID, "txn", it.TransactionID, "sandbox", res.Sandbox)
			s.trackServer(u.ID, "purchase_paid")
			break
		}
		ours = true
	}
	if !granted && !ours && other {
		writeErr(w, http.StatusConflict, "already_redeemed", "该购买已被其它账号使用")
		return
	}
	fresh, _ := s.store.UserByID(ctx, u.ID)
	if fresh == nil {
		fresh = u
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "granted": granted, "user": s.viewOf(ctx, fresh)})
}

// handleIAPRestore: POST /api/v1/iap/restore {receipt}
//
// "恢复购买" on a new phone. Plans are non-renewing subscriptions: Apple keeps
// them in the app receipt of the Apple ID that bought them, but our accounts
// are per device. So for every plan purchase in the receipt:
//   - never redeemed (e.g. the app died before verifying): granted to this device;
//   - redeemed by this device: nothing to do;
//   - redeemed by another device (the old phone): this device joins that
//     account's subscription, like the 多设备 feature, so the time already
//     paid is shared rather than granted twice.
func (s *Server) handleIAPRestore(w http.ResponseWriter, r *http.Request, u *store.User) {
	var req struct {
		Receipt string `json:"receipt"`
	}
	if !decode(w, r, &req) {
		return
	}
	if s.iap == nil {
		writeErr(w, http.StatusBadRequest, "iap_not_configured", "App 内购买未配置")
		return
	}
	if strings.TrimSpace(req.Receipt) == "" {
		writeErr(w, http.StatusBadRequest, "no_purchases", "没有找到可恢复的购买")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := s.iap.Verify(ctx, req.Receipt)
	if err != nil {
		slog.Warn("iap restore verify failed", "user", u.ID, "err", err)
		writeErr(w, http.StatusBadGateway, "verify_failed", "购买凭证校验失败，请稍后重试")
		return
	}
	if s.refuseIAPResult(w, res) {
		return
	}
	cat := s.cat()
	byProduct := map[string]*config.Plan{}
	for i := range cat.Plans {
		p := &cat.Plans[i]
		if p.ID != cat.FreePlan {
			byProduct[p.IAPProductID()] = p
		}
	}
	granted, found := 0, 0
	var owner int64 // another account that owns the newest redeemed purchase
	items := append([]iapItem(nil), res.Items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].PurchasedAt < items[j].PurchasedAt }) // oldest first
	for _, it := range items {
		plan := byProduct[it.ProductID]
		if plan == nil || it.Cancelled {
			continue
		}
		found++
		orderID := "AIAP" + sanitizeTxn(it.TransactionID)
		o, created, err := s.store.EnsureOrder(ctx, orderID, u.ID, plan.ID, plan.PriceCents, iapMethod)
		if err != nil {
			s.internal(w, err)
			return
		}
		if created || (o.UserID == u.ID && o.Status != "paid") {
			ok, err := s.store.MarkPaid(ctx, orderID, iapTradeNo(res, it.TransactionID), plan.DurationDays, time.Now())
			if err != nil {
				s.internal(w, err)
				return
			}
			if ok {
				granted++
			}
		} else if o.UserID != u.ID {
			owner = o.UserID // sorted oldest first: the newest wins
		}
	}
	if found == 0 {
		writeErr(w, http.StatusNotFound, "no_purchases", "当前 Apple ID 没有可恢复的购买")
		return
	}
	linked := ""
	if granted == 0 && owner != 0 && owner != u.ID && (!u.Linked() || u.PrimaryID != owner) {
		ou, err := s.store.UserByID(ctx, owner)
		if err == nil && ou.Linked() {
			owner = ou.PrimaryID // the old phone itself followed another account
		}
		if owner != u.ID {
			err = s.store.LinkDevice(ctx, u.ID, owner, cat.MultiDevice.MaxDevices-1)
			switch {
			case errors.Is(err, store.ErrDeviceLimit):
				writeErr(w, http.StatusConflict, "device_limit",
					fmt.Sprintf("原账号已绑定 %d 台设备（上限），请先在原设备的「多设备」里移除不用的设备", cat.MultiDevice.MaxDevices))
				return
			case errors.Is(err, store.ErrHasMembers):
				writeErr(w, http.StatusConflict, "has_members", "本机正在分享自己的订阅，请先移除本机绑定的其它设备")
				return
			case err != nil && !errors.Is(err, store.ErrSelfLink):
				s.internal(w, err)
				return
			}
			linked = rechargeCode(owner)
			slog.Info("iap restore linked device", "user", u.ID, "owner", owner)
		}
	}
	fresh, _ := s.store.UserByID(ctx, u.ID)
	if fresh == nil {
		fresh = u
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "granted": granted, "linked_to": linked, "user": s.viewOf(ctx, fresh)})
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
	ProductID          string `json:"product_id"`
	TransactionID      string `json:"transaction_id"`
	PurchaseDateMs     string `json:"purchase_date_ms"`
	CancellationDateMs string `json:"cancellation_date_ms"` // set when Apple refunded / revoked it
}

func (v *appleVerifier) Verify(ctx context.Context, receipt string) (*iapResult, error) {
	rec, err := v.post(ctx, v.prodURL, receipt)
	if err != nil {
		return nil, err
	}
	// 21007: a sandbox receipt sent to production (TestFlight / App Review).
	sandbox := false
	if rec.Status == 21007 {
		if rec, err = v.post(ctx, v.sandURL, receipt); err != nil {
			return nil, err
		}
		sandbox = true
	}
	if rec.Status != 0 {
		return nil, fmt.Errorf("apple verifyReceipt status %d", rec.Status)
	}
	out := &iapResult{BundleID: rec.Receipt.BundleID, Sandbox: sandbox}
	seen := map[string]int{} // a transaction can be in both lists
	for _, row := range append(append([]appleInAppRow(nil), rec.Receipt.InApp...), rec.LatestReceiptInfo...) {
		if row.ProductID == "" || row.TransactionID == "" {
			continue
		}
		at, _ := strconv.ParseInt(row.PurchaseDateMs, 10, 64)
		it := iapItem{ProductID: row.ProductID, TransactionID: row.TransactionID, PurchasedAt: at, Cancelled: row.CancellationDateMs != ""}
		if i, dup := seen[it.TransactionID]; dup {
			out.Items[i].Cancelled = out.Items[i].Cancelled || it.Cancelled
			continue
		}
		seen[it.TransactionID] = len(out.Items)
		out.Items = append(out.Items, it)
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
