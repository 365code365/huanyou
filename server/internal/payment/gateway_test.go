package payment

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGatewayLinkAndEnvelope(t *testing.T) {
	mid, sk, ek, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	g, err := NewGateway("https://pay.example.com/checkout", mid, sk, ek)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := g.PayURL(Order{ID: "VP1", AmountCents: 2500, Subject: "月套餐"}, "https://a/n", "https://a/q", "https://a/r")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("out_trade_no") != "VP1" || q.Get("amount") != "2500" {
		t.Fatalf("params: %v", q)
	}
	if err := g.VerifyLink(q, time.Now()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	q.Set("amount", "1")
	if err := g.VerifyLink(q, time.Now()); err != ErrBadSignature {
		t.Fatalf("tampered amount accepted: %v", err)
	}

	// The payment center seals a notification with the same keys.
	center, _ := NewGateway("", mid, sk, ek)
	env, err := center.Seal(GatewayOrder{OutTradeNo: "VP1", Amount: 2500, Status: StatusPaid})
	if err != nil {
		t.Fatal(err)
	}
	var got GatewayOrder
	if err := g.Open(env, &got); err != nil || got.Status != StatusPaid || got.Amount != 2500 {
		t.Fatalf("open: %v %+v", err, got)
	}
	if err := g.Open(env, &got); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("replay accepted: %v", err)
	}
	env2, _ := center.Seal(GatewayOrder{OutTradeNo: "VP1", Amount: 2500})
	env2.Data = env2.Data[:len(env2.Data)-4] + "AAAA"
	if err := g.Open(env2, &got); err != ErrBadSignature {
		t.Fatalf("tampered data: %v", err)
	}
	_, _, other, _ := NewKeys()
	bad, _ := NewGateway("", mid, sk, other)
	env3, _ := bad.Seal(GatewayOrder{OutTradeNo: "VP1"})
	if err := g.Open(env3, &got); err == nil {
		t.Fatal("wrong enc key accepted")
	}
}
