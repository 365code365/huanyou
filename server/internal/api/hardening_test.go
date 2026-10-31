package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"vpnplatform/server/internal/config"
)

func TestRedactForAI(t *testing.T) {
	in := `10-31 15:04:05 WARN guard: ip banned ip=203.0.113.9 path=/x mail=a.b@example.com peer=[2001:db8::7]:443 v=1.2.4 id=deadbeef`
	out := redactForAI(in)
	for _, leak := range []string{"203.0.113.9", "2001:db8::7", "a.b@example.com"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q still in %q", leak, out)
		}
	}
	for _, keep := range []string{"15:04:05", "v=1.2.4", "id=deadbeef", ":443", "<email>"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("%q lost from %q", keep, out)
		}
	}
	// Stable pseudonyms: the same address always maps to the same name.
	if redactForAI("ip=203.0.113.9") != redactForAI("ip=203.0.113.9") {
		t.Fatal("pseudonym not stable")
	}
	if got := logPath("/admin/api/x\nFAKE line"); strings.ContainsAny(got, "\n\r") {
		t.Fatalf("control characters kept: %q", got)
	}
	// Go's own error formats: "addr:port:" and an address ending a sentence.
	for _, in := range []string{
		"http: TLS handshake error from 203.0.113.5:51234: EOF",
		`err="read tcp 10.0.0.2:3306->198.51.100.7:44122: i/o timeout"`,
		"blocked 203.0.113.5.",
		"peer [2001:db8::1]:443: reset",
		"v6 2001:db8::5.",
	} {
		out := redactForAI(in)
		for _, leak := range []string{"203.0.113.5", "198.51.100.7", "10.0.0.2", "2001:db8::1", "2001:db8::5"} {
			if strings.Contains(out, leak) {
				t.Errorf("redactForAI(%q) = %q: %s left in", in, out, leak)
			}
		}
	}
	if got := redactForAI("at 21:30:08.158966 ok"); got != "at 21:30:08.158966 ok" {
		t.Errorf("timestamp changed: %q", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := withSecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for path, framed := range map[string]bool{"/admin/": false, "/api/v1/plans": true} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: no nosniff", path)
		}
		if (rec.Header().Get("X-Frame-Options") == "") != framed {
			t.Fatalf("%s: X-Frame-Options=%q", path, rec.Header().Get("X-Frame-Options"))
		}
	}
}

func TestLimiterIPv6PerPrefix(t *testing.T) {
	l := &ipLimiter{every: rate.Every(time.Hour), burst: 2, entries: map[string]*limiterEntry{}}
	allowed := 0
	for i := 1; i <= 10; i++ {
		if l.allowIP("admin:", "2001:db8:1:2::"+strconv.Itoa(i)) {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("one /64 got %d attempts, want 2", allowed)
	}
	if !l.allowIP("admin:", "2001:db8:1:3::1") || !l.allowIP("admin:", "203.0.113.5") {
		t.Fatal("other networks must have their own budget")
	}
}

func TestNonceCacheGenerations(t *testing.T) {
	var c nonceCache
	now := int64(1_800_000_000)
	if c.seen("1:a", now) {
		t.Fatal("new nonce reported seen")
	}
	gen := int64(2 * sigWindow / time.Second)
	// Still remembered for at least two signature windows ...
	if !c.seen("1:a", now+gen-1) || !c.seen("1:a", now+gen+1) {
		t.Fatal("nonce forgotten too early")
	}
	// ... and dropped after two generations.
	c.seen("1:x", now+2*gen+1)
	if c.seen("1:a", now+3*gen+2) {
		t.Fatal("nonce kept forever")
	}
}

func TestIAPRefusals(t *testing.T) {
	e := newIAPEnv(t, 0) // no build under review: sandbox receipts are test purchases
	e.fk.byReceipt["sand"] = []iapItem{{ProductID: "month", TransactionID: "1000001"}}
	e.fk.byReceipt["refunded"] = []iapItem{{ProductID: "month", TransactionID: "1000002", Cancelled: true}}
	e.fk.byReceipt["older-free"] = []iapItem{
		{ProductID: "month", TransactionID: "1000003", PurchasedAt: 1},
		{ProductID: "month", TransactionID: "1000004", PurchasedAt: 2},
	}
	sandbox := &sandboxIAP{fakeIAP: e.fk}
	e.srv.iap = sandbox

	m := e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "sand"}, 400, nil)
	if m["error"] != "sandbox_receipt" {
		t.Fatalf("sandbox outside review: %v", m)
	}
	sandbox.off = true
	m = e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "refunded"}, 400, nil)
	if m["error"] != "no_matching_purchase" {
		t.Fatalf("refunded purchase: %v", m)
	}
	// Two purchases: the newest one is redeemed first, the older one next time.
	m = e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "older-free"}, 200, nil)
	if m["granted"] != true {
		t.Fatalf("first: %v", m)
	}
	if o, err := e.srv.store.OrderByID(t.Context(), "AIAP1000004"); err != nil || o.Status != "paid" {
		t.Fatalf("newest not redeemed first: %+v %v", o, err)
	}
	m = e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "older-free"}, 200, nil)
	if m["granted"] != true {
		t.Fatalf("second: %v", m)
	}
	m = e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "older-free"}, 200, nil)
	if m["granted"] != false {
		t.Fatalf("third must grant nothing: %v", m)
	}
	// A receipt of another app.
	e.fk.bundleID = "com.other.app"
	m = e.call("POST", "/api/v1/iap/verify", e.tok, map[string]string{"plan_id": "month", "receipt": "older-free"}, 400, nil)
	if m["error"] != "bad_receipt" {
		t.Fatalf("other app: %v", m)
	}
}

// sandboxIAP marks fake receipts as sandbox ones (until off).
type sandboxIAP struct {
	*fakeIAP
	off bool
}

func (s *sandboxIAP) Verify(ctx context.Context, receipt string) (*iapResult, error) {
	r, err := s.fakeIAP.Verify(ctx, receipt)
	if r != nil {
		r.Sandbox = !s.off
	}
	return r, err
}

func TestTrafficBatchCountedOnce(t *testing.T) {
	e := newIAPEnv(t, 0)
	const tok = "node-token-0123456789abcdef-xyz"
	if _, err := e.srv.live.Update(func(c *config.Catalog) error {
		c.Nodes = append(c.Nodes, config.Node{ID: "n1", Name: "测试", Address: "203.0.113.1", VisionPort: 443, Token: tok,
			Reality: config.RealityPublic{PublicKey: "pk", ShortID: "0123456789abcdef", ServerName: "www.apple.com"}})
		return nil
	}, func(*config.Catalog) error { return nil }); err != nil {
		t.Fatal(err)
	}
	u, err := e.srv.store.DeviceUser(t.Context(), "and-traffic-0001", "android")
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"batch": "b-00000001", "traffic": []map[string]any{{"email": "u" + strconv.FormatInt(u.ID, 10), "up": 1000, "down": 5000}}}
	e.call("POST", "/internal/v1/traffic", tok, body, 200, nil)
	m := e.call("POST", "/internal/v1/traffic", tok, body, 200, nil) // the agent's retry after a lost response
	if m["duplicate"] != true {
		t.Fatalf("retry not recognised: %v", m)
	}
	body["batch"] = "b-00000002"
	body["traffic"] = []map[string]any{{"email": "u" + strconv.FormatInt(u.ID, 10), "up": 1 << 62, "down": 1}} // absurd value ignored
	e.call("POST", "/internal/v1/traffic", tok, body, 200, nil)
	fresh, _ := e.srv.store.UserByID(t.Context(), u.ID)
	if fresh.TrafficUsed != 6000 {
		t.Fatalf("traffic_used = %d, want 6000", fresh.TrafficUsed)
	}
}

// Every user of a node shares its address: a ban earned through the tunnel
// must not cut the agent (valid token) off from its user list.
func TestGuardSparesNodeAgents(t *testing.T) {
	e := newIAPEnv(t, 0)
	const tok = "node-token-guard-0123456789abcdef"
	if _, err := e.srv.live.Update(func(c *config.Catalog) error {
		c.Nodes = append(c.Nodes, config.Node{ID: "n1", Name: "测试", Address: "203.0.113.1", VisionPort: 443, Token: tok,
			Reality: config.RealityPublic{PublicKey: "pk", ShortID: "0123456789abcdef", ServerName: "www.apple.com"}})
		return nil
	}, func(*config.Catalog) error { return nil }); err != nil {
		t.Fatal(err)
	}
	h := e.srv.withGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	e.srv.guard.mu.Lock()
	if e.srv.guard.ips == nil {
		e.srv.guard.ips = map[string]*guardEntry{}
	}
	e.srv.guard.ips["203.0.113.50"] = &guardEntry{bannedU: time.Now().Unix() + 3600}
	e.srv.guard.mu.Unlock()
	get := func(path, token string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.RemoteAddr = "203.0.113.50:40000"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if c := get("/internal/v1/users", tok); c != http.StatusOK {
		t.Fatalf("node agent with a valid token: %d", c)
	}
	if c := get("/internal/v1/users", "guess"); c != http.StatusForbidden {
		t.Fatalf("banned address, bad token: %d", c)
	}
	if c := get("/api/v1/plans", tok); c != http.StatusForbidden {
		t.Fatalf("node token outside /internal/: %d", c)
	}
}
