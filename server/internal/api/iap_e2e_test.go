package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"vpnplatform/server/internal/auth"
	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/monitor"
	"vpnplatform/server/internal/payment"
	"vpnplatform/server/internal/store"
)

// fakeIAP is a stand-in for Apple's verifyReceipt: the test controls what a
// given "receipt" string decodes to.
type fakeIAP struct {
	bundleID  string
	byReceipt map[string][]iapItem
}

func (f *fakeIAP) Verify(_ context.Context, receipt string) (*iapResult, error) {
	return &iapResult{BundleID: f.bundleID, Items: f.byReceipt[receipt]}, nil
}

type iapEnv struct {
	t   *testing.T
	api *httptest.Server
	srv *Server
	fk  *fakeIAP
	tok string
}

func newIAPEnv(t *testing.T, reviewBuild int) *iapEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "vpn.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cat := &config.Catalog{
		FreePlan: "free",
		Plans: []config.Plan{
			{ID: "free", Name: "免费版", TrafficGB: 1},
			{ID: "month", Name: "月套餐", PriceCents: 2500, DurationDays: 30},
			{ID: "year", Name: "年套餐", PriceCents: 24000, DurationDays: 365, IOSProductID: "com.huanyou.year"},
		},
		Apps: []config.AppRec{{ID: "a1", Name: "示例", URL: "https://example.com"}},
	}
	// A configured gateway so the non-iOS face has a real method.
	mid, sk, ek, _ := payment.NewKeys()
	cat.Payment = config.PaymentConfig{
		IOSReviewBuild: reviewBuild,
		Gateway: config.GatewayConfig{Enabled: true, Name: "在线支付", PayLink: "https://pay.example.com/checkout",
			MerchantID: mid, SignKey: sk, EncKey: ek},
	}
	if err := cat.Normalize(); err != nil {
		t.Fatal(err)
	}
	live := config.NewLiveCatalog(cat)
	issuer, _ := auth.NewIssuer("test-secret-0123456789abcdef0123456789", time.Hour)
	cfg := &config.API{}
	cfg.Payments.AppleIAP = &config.AppleIAPConfig{SharedSecret: "s", BundleID: "com.huanyou.vpn"}
	api := httptest.NewUnstartedServer(nil)
	cfg.PublicURL = "http://" + api.Listener.Addr().String()
	srv, err := New(cfg, live, st, issuer, monitor.New(live))
	if err != nil {
		t.Fatal(err)
	}
	fk := &fakeIAP{bundleID: "com.huanyou.vpn", byReceipt: map[string][]iapItem{}}
	srv.iap = fk
	api.Config.Handler = srv.Handler()
	api.Start()
	t.Cleanup(api.Close)
	e := &iapEnv{t: t, api: api, srv: srv, fk: fk}
	var tok struct{ Token string }
	e.call("POST", "/api/v1/auth/device", "", map[string]string{"device_id": "iap-device-0001", "platform": "ios"}, 200, &tok)
	e.tok = tok.Token
	return e
}

func (e *iapEnv) call(method, path, token string, body any, want int, out any) map[string]any {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.api.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s: HTTP %d (want %d): %s", method, path, resp.StatusCode, want, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	return m
}

func TestIOSReviewFace(t *testing.T) {
	e := newIAPEnv(t, 7) // build 7 is under App Review

	t.Run("android sees the gateway face", func(t *testing.T) {
		m := e.call("GET", "/api/v1/plans?platform=android&build=7", "", nil, 200, nil)
		if m["pay_mode"] != "gateway" {
			t.Fatalf("pay_mode=%v", m["pay_mode"])
		}
		methods := m["methods"].([]any)
		if len(methods) == 0 || methods[0].(map[string]any)["id"] != "gateway" {
			t.Fatalf("methods=%v", methods)
		}
	})

	t.Run("old iOS build sees the gateway face", func(t *testing.T) {
		m := e.call("GET", "/api/v1/plans?platform=ios&build=6", "", nil, 200, nil)
		if m["pay_mode"] != "gateway" {
			t.Fatalf("pay_mode=%v", m["pay_mode"])
		}
	})

	t.Run("iOS review build sees the IAP face", func(t *testing.T) {
		m := e.call("GET", "/api/v1/plans?platform=ios&build=7", "", nil, 200, nil)
		if m["pay_mode"] != "iap" {
			t.Fatalf("pay_mode=%v", m["pay_mode"])
		}
		methods := m["methods"].([]any)
		if len(methods) != 1 || methods[0].(map[string]any)["id"] != "iap" {
			t.Fatalf("methods=%v", methods)
		}
		if m["purchase"].(map[string]any)["enabled"] != true {
			t.Fatalf("purchase=%v", m["purchase"])
		}
		// Product ids are exposed only on the IAP face.
		for _, p := range m["plans"].([]any) {
			pm := p.(map[string]any)
			switch pm["id"] {
			case "month":
				if pm["iap_product_id"] != "month" {
					t.Fatalf("month product id=%v", pm["iap_product_id"])
				}
			case "year":
				if pm["iap_product_id"] != "com.huanyou.year" {
					t.Fatalf("year product id=%v", pm["iap_product_id"])
				}
			case "free":
				if _, ok := pm["iap_product_id"]; ok {
					t.Fatal("free plan should carry no product id")
				}
			}
		}
	})

	t.Run("web order refused on the IAP face", func(t *testing.T) {
		e.call("POST", "/api/v1/orders?platform=ios&build=7", e.tok,
			map[string]string{"plan_id": "month", "method": "gateway"}, 409, nil)
		// but allowed for the same user on android
		var r struct {
			Order store.Order `json:"order"`
		}
		e.call("POST", "/api/v1/orders?platform=android&build=7", e.tok,
			map[string]string{"plan_id": "month", "method": "gateway"}, 200, &r)
		if r.Order.ID == "" {
			t.Fatal("android order not created")
		}
	})

	t.Run("recommended apps hidden on the IAP face", func(t *testing.T) {
		var ios, and struct {
			Apps []map[string]any `json:"apps"`
		}
		e.call("GET", "/api/v1/apps?platform=ios&build=7", "", nil, 200, &ios)
		e.call("GET", "/api/v1/apps?platform=android&build=7", "", nil, 200, &and)
		if len(ios.Apps) != 0 {
			t.Fatalf("iOS review face should show no apps, got %d", len(ios.Apps))
		}
		if len(and.Apps) != 1 {
			t.Fatalf("android should show apps, got %d", len(and.Apps))
		}
	})

	t.Run("iap verify grants once", func(t *testing.T) {
		e.fk.byReceipt["r-month"] = []iapItem{{ProductID: "month", TransactionID: "1000000777"}}
		m := e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "r-month"}, 200, nil)
		if m["granted"] != true {
			t.Fatalf("first verify granted=%v", m["granted"])
		}
		ent := m["user"].(map[string]any)["entitlement"].(map[string]any)
		if ent["plan_id"] != "month" || ent["paid"] != true {
			t.Fatalf("entitlement=%v", ent)
		}
		// Re-verify (app retry / restore): no second grant.
		m = e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "r-month"}, 200, nil)
		if m["granted"] != false {
			t.Fatalf("re-verify granted=%v", m["granted"])
		}
	})

	t.Run("iap verify rejects a receipt without the plan", func(t *testing.T) {
		e.fk.byReceipt["r-other"] = []iapItem{{ProductID: "someone-else", TransactionID: "x1"}}
		e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "year", "receipt": "r-other"}, 400, nil)
	})

	t.Run("iap verify rejects a wrong-bundle receipt", func(t *testing.T) {
		e.fk.bundleID = "com.someone.else"
		e.fk.byReceipt["r-bad"] = []iapItem{{ProductID: "year", TransactionID: "y1"}}
		e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "year", "receipt": "r-bad"}, 400, nil)
		e.fk.bundleID = "com.huanyou.vpn"
	})

	t.Run("flip the review build off: iOS returns to the gateway face", func(t *testing.T) {
		_, err := e.srv.live.Update(func(c *config.Catalog) error { c.Payment.IOSReviewBuild = 0; return nil }, func(*config.Catalog) error { return nil })
		if err != nil {
			t.Fatal(err)
		}
		m := e.call("GET", "/api/v1/plans?platform=ios&build=7", "", nil, 200, nil)
		if m["pay_mode"] != "gateway" {
			t.Fatalf("after flip pay_mode=%v", m["pay_mode"])
		}
	})
}
