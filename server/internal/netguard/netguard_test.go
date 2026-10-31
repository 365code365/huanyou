package netguard

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestBlocked(t *testing.T) {
	for _, s := range []string{"169.254.169.254", "fe80::1", "fd00:ec2::254", "100.100.100.200", "0.0.0.0", "::", "::ffff:169.254.169.254", "224.0.0.1"} {
		if !Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "192.168.1.10", "8.8.8.8", "2606:4700::1111"} {
		if Blocked(netip.MustParseAddr(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestClientRefusesMetadataAndRedirects(t *testing.T) {
	c := Client(5 * time.Second)
	if _, err := c.Get("http://169.254.169.254/latest/meta-data/"); err == nil || !strings.Contains(err.Error(), "netguard") {
		t.Fatalf("metadata request: %v", err)
	}
	// A redirect to the metadata service comes back as the redirect itself.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status %d, want the 302 unfollowed", resp.StatusCode)
	}
}
