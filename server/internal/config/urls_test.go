package config

import "testing"

func TestCheckChangedURLs(t *testing.T) {
	// Stored under the older prefix rules: must keep loading and saving.
	prev := &Catalog{}
	prev.Payment.Gateway.QueryURL = "http://localhost.localdomain:8080/q"
	same := &Catalog{}
	same.Payment.Gateway.QueryURL = prev.Payment.Gateway.QueryURL
	if err := same.CheckChangedURLs(prev); err != nil {
		t.Fatalf("unchanged stored address refused: %v", err)
	}
	for _, c := range []struct {
		webhook, payLink string
		ok               bool
	}{
		{"https://user:pass@ntfy.example.com/alerts", "", true}, // basic auth in a webhook is fine
		{"", "https://pay.example.com/checkout", true},
		{"", "http://127.0.0.1:18200/pay", true},
		{"", "http://localhost.evil.example/pay", false},
		{"", "http://localhost@evil.example/pay", false},
		{"ftp://example.com/x", "", false},
	} {
		next := &Catalog{}
		next.Monitor.AlertWebhook = c.webhook
		next.Payment.Gateway.PayLink = c.payLink
		if err := next.CheckChangedURLs(prev); (err == nil) != c.ok {
			t.Errorf("webhook %q pay %q: err=%v, want ok=%v", c.webhook, c.payLink, err, c.ok)
		}
	}
}
