package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vpnplatform/server/internal/auth"
	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/monitor"
	"vpnplatform/server/internal/payment"
	"vpnplatform/server/internal/payment/paycenter"
	"vpnplatform/server/internal/store"
)

// End-to-end test of the "下单链接" integration with an external payment
// center (internal/payment/paycenter simulates the center):
//
//	app creates order -> opens the center's checkout link (center verifies the
//	signature) -> user pays -> center notifies us -> plan granted
//	notify lost -> our active query to the center completes the order
//	forged / replayed / wrong-amount / wrong-method notifications are refused
type gwEnv struct {
	t      *testing.T
	api    *httptest.Server
	center *paycenter.Center
	cs     *httptest.Server
	srv    *Server
	st     *store.Store
	mid    string
	sk, ek string
	token  string
}

func newGWEnv(t *testing.T) *gwEnv {
	t.Helper()
	mid, sk, ek, err := payment.NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	center, err := paycenter.New(mid, sk, ek)
	if err != nil {
		t.Fatal(err)
	}
	center.Backoff = 50 * time.Millisecond
	cs := httptest.NewServer(center.Handler())
	t.Cleanup(cs.Close)

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
		},
	}
	cat.Payment.Gateway = config.GatewayConfig{Enabled: true, Name: "在线支付",
		PayLink: cs.URL + "/checkout", QueryURL: cs.URL + "/api/order/query",
		MerchantID: mid, SignKey: sk, EncKey: ek}
	if err := cat.Normalize(); err != nil {
		t.Fatal(err)
	}
	live := config.NewLiveCatalog(cat)
	issuer, err := auth.NewIssuer("test-secret-0123456789abcdef0123456789", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.API{Admin: config.AdminConfig{Password: "admin-pass-123"}}
	cfg.Payments.Mock = true // a second provider, to check method binding
	api := httptest.NewUnstartedServer(nil)
	cfg.PublicURL = "http://" + api.Listener.Addr().String()
	srv, err := New(cfg, live, st, issuer, monitor.New(live))
	if err != nil {
		t.Fatal(err)
	}
	api.Config.Handler = srv.Handler()
	api.Start()
	t.Cleanup(api.Close)

	e := &gwEnv{t: t, api: api, center: center, cs: cs, srv: srv, st: st, mid: mid, sk: sk, ek: ek}
	var tok struct{ Token string }
	e.call("POST", "/api/v1/auth/device", "", map[string]string{"device_id": "e2e-device-0001", "platform": "android"}, 200, &tok)
	e.token = tok.Token
	return e
}

func (e *gwEnv) call(method, path, token string, body any, want int, out any) {
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
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		e.t.Fatalf("%s %s: HTTP %d (want %d): %s", method, path, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}

// order creates an order and opens its checkout link at the center.
func (e *gwEnv) order() (id, payURL string) {
	e.t.Helper()
	var r struct {
		Order  store.Order `json:"order"`
		PayURL string      `json:"pay_url"`
	}
	e.call("POST", "/api/v1/orders", e.token, map[string]string{"plan_id": "month", "method": "gateway"}, 200, &r)
	if !strings.HasPrefix(r.PayURL, e.cs.URL+"/checkout?") {
		e.t.Fatalf("pay_url %q does not point at the center", r.PayURL)
	}
	resp, err := http.Get(r.PayURL)
	if err != nil {
		e.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), r.Order.ID) || !strings.Contains(string(b), "25.00") {
		e.t.Fatalf("checkout page: HTTP %d %s", resp.StatusCode, b)
	}
	return r.Order.ID, r.PayURL
}

func (e *gwEnv) status(id string) string {
	var o store.Order
	e.call("GET", "/api/v1/orders/"+id, e.token, nil, 200, &o)
	return o.Status
}

func (e *gwEnv) waitPaid(id string, within time.Duration) {
	e.t.Helper()
	end := time.Now().Add(within)
	for time.Now().Before(end) {
		if e.status(id) == "paid" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("order %s not paid within %s", id, within)
}

func (e *gwEnv) expiry() time.Time {
	var me userView
	e.call("GET", "/api/v1/me", e.token, nil, 200, &me)
	if me.Entitlement.PlanID != "month" || me.Entitlement.ExpiresAt == nil {
		e.t.Fatalf("plan not granted: %+v", me.Entitlement)
	}
	return *me.Entitlement.ExpiresAt
}

// notify posts a sealed notification as the center (or an attacker with keys k).
func (e *gwEnv) notify(env *payment.Envelope) (int, string) {
	b, _ := json.Marshal(env)
	resp, err := http.Post(e.api.URL+gatewayNotifyPath, "application/json", bytes.NewReader(b))
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (e *gwEnv) sealer(encKey string) *payment.Gateway {
	g, err := payment.NewGateway("", "", e.mid, e.sk, encKey)
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

func TestGatewayEndToEnd(t *testing.T) {
	e := newGWEnv(t)

	// The app sees the gateway as a payment method.
	var plans struct {
		Methods []payment.Method `json:"methods"`
	}
	e.call("GET", "/api/v1/plans", "", nil, 200, &plans)
	if len(plans.Methods) == 0 || plans.Methods[0].ID != "gateway" {
		t.Fatalf("methods: %+v", plans.Methods)
	}

	t.Run("tampered link refused by the center", func(t *testing.T) {
		_, payURL := e.order()
		u, _ := url.Parse(payURL)
		q := u.Query()
		q.Set("amount", "1")
		u.RawQuery = q.Encode()
		resp, err := http.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("tampered amount: HTTP %d", resp.StatusCode)
		}
	})

	var firstExpiry time.Time
	t.Run("paid and notified", func(t *testing.T) {
		id, _ := e.order()
		if e.status(id) != "pending" {
			t.Fatal("new order not pending")
		}
		if _, err := e.center.Pay(id); err != nil {
			t.Fatal(err)
		}
		e.waitPaid(id, 5*time.Second)
		firstExpiry = e.expiry()
		time.Sleep(200 * time.Millisecond)
		if o, _ := e.center.Order(id); !o.Notified || o.NotifyAttempts != 1 {
			t.Fatalf("center notify state: %+v", o)
		}
		// The center repeats the notification (its retry): idempotent.
		if err := e.center.Notify(id); err != nil {
			t.Fatalf("repeated notify not acknowledged: %v", err)
		}
		if got := e.expiry(); !got.Equal(firstExpiry) {
			t.Fatalf("repeated notify granted again: %s -> %s", firstExpiry, got)
		}
	})

	t.Run("notify lost, active query completes the order", func(t *testing.T) {
		e.center.DropNotify = true
		defer func() { e.center.DropNotify = false }()
		id, _ := e.order()
		if _, err := e.center.Pay(id); err != nil {
			t.Fatal(err)
		}
		// Too new to query (avoids hammering the center right after ordering).
		if e.status(id) != "pending" {
			t.Fatal("paid before any notify or query")
		}
		time.Sleep(3200 * time.Millisecond)
		if s := e.status(id); s != "paid" { // the app polling the order triggers our query
			t.Fatalf("active query did not complete the order: %s", s)
		}
		if o, _ := e.center.Order(id); o.NotifyAttempts != 0 {
			t.Fatalf("center should not have notified: %+v", o)
		}
		if got := e.expiry(); !got.After(firstExpiry.Add(29 * 24 * time.Hour)) {
			t.Fatalf("second month not added: %s -> %s", firstExpiry, got)
		}
		// The late notification still gets SUCCESS and grants nothing more.
		before := e.expiry()
		if err := e.center.Notify(id); err != nil {
			t.Fatalf("late notify: %v", err)
		}
		if got := e.expiry(); !got.Equal(before) {
			t.Fatal("late notify granted again")
		}
	})

	t.Run("background reconciler completes old orders without the app", func(t *testing.T) {
		e.center.DropNotify = true
		defer func() { e.center.DropNotify = false }()
		id, _ := e.order()
		if _, err := e.center.Pay(id); err != nil {
			t.Fatal(err)
		}
		// The app is closed; the reconciler runs a few minutes later.
		time.Sleep(3200 * time.Millisecond)
		e.srv.reconcileAt(t.Context(), time.Now().Add(5*time.Minute), 1)
		o, err := e.st.OrderByID(t.Context(), id)
		if err != nil || o.Status != "paid" {
			t.Fatalf("reconciler: %v %+v", err, o)
		}
	})

	t.Run("unpaid order stays pending after a query", func(t *testing.T) {
		id, _ := e.order()
		time.Sleep(3200 * time.Millisecond)
		if s := e.status(id); s != "pending" {
			t.Fatalf("unpaid order became %s", s)
		}
	})

	t.Run("forged, replayed and wrong-amount notifications are refused", func(t *testing.T) {
		id, _ := e.order()
		_, _, otherKey, _ := payment.NewKeys()
		forged, _ := e.sealer(otherKey).Seal(payment.GatewayOrder{OutTradeNo: id, Amount: 2500, Status: "PAID"})
		if code, body := e.notify(forged); code != 401 {
			t.Fatalf("forged (wrong key): %d %s", code, body)
		}
		good := e.sealer(e.ek)
		low, _ := good.Seal(payment.GatewayOrder{OutTradeNo: id, Amount: 1, Currency: "CNY", Status: "PAID"})
		if code, body := e.notify(low); code != 400 || !strings.Contains(body, "REJECTED") {
			t.Fatalf("wrong amount: %d %s", code, body)
		}
		if e.status(id) != "pending" {
			t.Fatal("wrong amount completed the order")
		}
		ok, _ := good.Seal(payment.GatewayOrder{OutTradeNo: id, Amount: 2500, Currency: "CNY", Status: "PAID", TradeNo: "T1"})
		if code, body := e.notify(ok); code != 200 || !strings.Contains(body, "SUCCESS") {
			t.Fatalf("valid notify: %d %s", code, body)
		}
		if code, body := e.notify(ok); code != 401 {
			t.Fatalf("replayed envelope accepted: %d %s", code, body)
		}
		stale, _ := good.Seal(payment.GatewayOrder{OutTradeNo: id, Amount: 2500, Status: "PAID"})
		stale.Timestamp -= 3600
		if code, _ := e.notify(stale); code != 401 {
			t.Fatalf("stale timestamp accepted: %d", code)
		}
		unknown, _ := good.Seal(payment.GatewayOrder{OutTradeNo: "VPNOPE", Amount: 2500, Status: "PAID"})
		if code, _ := e.notify(unknown); code != 404 {
			t.Fatalf("unknown order: %d", code)
		}
	})

	t.Run("gateway cannot complete an order placed with another method", func(t *testing.T) {
		var r struct {
			Order store.Order `json:"order"`
		}
		e.call("POST", "/api/v1/orders", e.token, map[string]string{"plan_id": "month", "method": "mock"}, 200, &r)
		env, _ := e.sealer(e.ek).Seal(payment.GatewayOrder{OutTradeNo: r.Order.ID, Amount: 2500, Status: "PAID"})
		if code, body := e.notify(env); code != 400 {
			t.Fatalf("cross-method notify: %d %s", code, body)
		}
		if e.status(r.Order.ID) != "pending" {
			t.Fatal("mock order completed through the gateway")
		}
	})

	t.Run("admin test query", func(t *testing.T) {
		var login struct{ Token string }
		e.call("POST", "/admin/api/login", "", map[string]string{"password": "admin-pass-123"}, 200, &login)
		var r struct {
			OK    bool                  `json:"ok"`
			Found bool                  `json:"found"`
			Order *payment.GatewayOrder `json:"order"`
		}
		e.call("POST", "/admin/api/payment/test-query", login.Token, map[string]string{"order_id": "VPNOTHING"}, 200, &r)
		if !r.OK || r.Found {
			t.Fatalf("unknown order: %+v", r)
		}
		id, _ := e.order()
		e.call("POST", "/admin/api/payment/test-query", login.Token, map[string]string{"order_id": id}, 200, &r)
		if !r.OK || !r.Found || r.Order.Status != "PENDING" || r.Order.Amount != 2500 {
			t.Fatalf("pending order: %+v", r)
		}
	})

	t.Run("center unreachable: query fails quietly, order stays pending", func(t *testing.T) {
		id, _ := e.order()
		e.cs.Close()
		time.Sleep(3200 * time.Millisecond)
		if s := e.status(id); s != "pending" {
			t.Fatalf("got %s", s)
		}
	})
}
