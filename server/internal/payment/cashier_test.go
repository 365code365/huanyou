package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testSign = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	testEnc  = "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
)

func newTestCashier(t *testing.T, url string) *Cashier {
	t.Helper()
	if url == "" {
		url = "https://pay.example.com"
	}
	c, err := NewCashier(url, "M1001", testSign, testEnc, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCashierSealOpenRoundTrip(t *testing.T) {
	c := newTestCashier(t, "")
	in := cashierOrder{OutTradeNo: "VP1", Amount: 2500, Status: StatusPaid}
	env, err := c.Seal(in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(env.Data, "VP1") {
		t.Fatal("payload is not encrypted")
	}
	var out cashierOrder
	if err := c.Open(env, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip mismatch: %+v", out)
	}
}

func TestCashierRejects(t *testing.T) {
	c := newTestCashier(t, "")
	now := time.Now()
	fresh := func() *Envelope {
		env, err := c.Seal(cashierOrder{OutTradeNo: "VP2", Amount: 100}, now)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	var out cashierOrder

	// Replay: the same envelope twice.
	env := fresh()
	if err := c.Open(env, &out, now); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(env, &out, now); err == nil {
		t.Fatal("replayed nonce accepted")
	}

	// Tampered ciphertext (re-signed by an attacker without the key: just flip a byte).
	env = fresh()
	b := []byte(env.Data)
	b[10] ^= 1
	env.Data = string(b)
	if err := c.Open(env, &out, now); err == nil {
		t.Fatal("tampered data accepted")
	}

	// Bad signature.
	env = fresh()
	env.Sign = strings.Repeat("0", 64)
	if err := c.Open(env, &out, now); err != ErrBadSignature {
		t.Fatalf("bad signature: got %v", err)
	}

	// Stale timestamp.
	old, _ := c.Seal(cashierOrder{OutTradeNo: "VP3"}, now.Add(-10*time.Minute))
	if err := c.Open(old, &out, now); err == nil {
		t.Fatal("stale timestamp accepted")
	}

	// Wrong merchant.
	env = fresh()
	env.MerchantID = "M9999"
	if err := c.Open(env, &out, now); err == nil {
		t.Fatal("wrong merchant accepted")
	}

	// A different encryption key with the right signing key: signature passes,
	// AES-GCM authentication must fail.
	other, _ := NewCashier("https://pay.example.com", "M1001", testSign,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	env, _ = other.Seal(cashierOrder{OutTradeNo: "VP4"}, now)
	if err := c.Open(env, &out, now); err == nil {
		t.Fatal("payload sealed with another key accepted")
	}
}

func TestCashierCreateQueryNotify(t *testing.T) {
	var cash *Cashier // the cashier side shares keys with the merchant side
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cashierOrder
		if err := cash.OpenRequest(r, &req); err != nil {
			w.WriteHeader(400)
			w.Write([]byte(`{"code":"BAD_SIGN","message":"` + err.Error() + `"}`))
			return
		}
		resp := req
		switch r.URL.Path {
		case "/v1/orders/create":
			resp.Status, resp.PayURL = StatusPending, "https://pay.example.com/c/"+req.OutTradeNo
		case "/v1/orders/query":
			resp.Amount, resp.Status, resp.TradeNo = 2500, StatusPaid, "T123"
		}
		env, _ := cash.Seal(resp, time.Now())
		json.NewEncoder(w).Encode(env)
	}))
	defer srv.Close()
	cash = newTestCashier(t, "http://127.0.0.1")
	merchant := newTestCashier(t, "http://127.0.0.1")
	merchant.APIURL = srv.URL

	u, err := merchant.PayURL(Order{ID: "VP5", AmountCents: 2500, Subject: "月套餐"}, "cashier.alipay",
		"https://api.example.com/pay/notify/cashier", "https://api.example.com/pay/return")
	if err != nil || u != "https://pay.example.com/c/VP5" {
		t.Fatalf("create: %q %v", u, err)
	}
	n, err := merchant.Query(context.Background(), "VP5")
	if err != nil || !n.Paid || n.AmountCents != 2500 || n.TradeNo != "T123" {
		t.Fatalf("query: %+v %v", n, err)
	}

	// Callback from the cashier.
	env, _ := cash.Seal(cashierOrder{OutTradeNo: "VP5", TradeNo: "T123", Amount: 2500, Currency: "CNY", Status: StatusPaid}, time.Now())
	b, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/pay/notify/cashier", bytes.NewReader(b))
	n, err = merchant.ParseNotify(req)
	if err != nil || !n.Paid || n.OrderID != "VP5" {
		t.Fatalf("notify: %+v %v", n, err)
	}
}
