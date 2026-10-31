// Package netguard is for outbound HTTP requests to addresses an admin can
// set (AI endpoint, alert webhook, payment-center query URL). Those must not
// become a way to reach the cloud provider's metadata service (instance
// credentials) or other link-local endpoints, for example with a stolen
// admin session: the address is checked after DNS resolution, so a hostname
// can't smuggle one in, and redirects are not followed. Loopback and private
// addresses stay allowed (self-hosted AI, a local payment center).
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

var blocked = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),     // link-local; AWS / GCP / Azure / DO metadata at 169.254.169.254
	netip.MustParsePrefix("fe80::/10"),          // IPv6 link-local
	netip.MustParsePrefix("fd00:ec2::254/128"),  // AWS metadata over IPv6
	netip.MustParsePrefix("100.100.100.200/32"), // Alibaba Cloud metadata
	netip.MustParsePrefix("100.80.80.80/32"),    // UCloud metadata
	netip.MustParsePrefix("100.96.0.96/32"),     // Volcengine metadata
	netip.MustParsePrefix("0.0.0.0/8"),
}

// Blocked reports whether a connection to a must be refused.
func Blocked(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range blocked {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Control is a net.Dialer.Control that refuses blocked addresses.
func Control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if Blocked(a) {
		return fmt.Errorf("netguard: connections to %s are not allowed", host)
	}
	return nil
}

// Client is an http.Client that refuses blocked addresses and returns
// redirects as they are instead of following them.
func Client(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: Control}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.DialContext
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
