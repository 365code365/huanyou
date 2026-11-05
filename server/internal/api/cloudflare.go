package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// When the API domain is proxied by Cloudflare (orange cloud), every request
// reaches us from a Cloudflare edge address, shared by many users. Rate
// limits, automatic bans, the new-device limit and IP records would then hit
// Cloudflare instead of the user: one scanner behind an edge would get the
// edge banned for everyone using it. With trust_cloudflare the real address
// is taken from CF-Connecting-IP, but only on connections that really come
// from Cloudflare's published ranges, so a client cannot forge it.

// cloudflareRanges: https://www.cloudflare.com/ips-v4 and /ips-v6 (2026-11).
var cloudflareRanges = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// isCloudflare reports whether ip is a Cloudflare edge address.
func isCloudflare(ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap()
	for _, p := range cloudflareRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// cloudflareClientIP is the visitor address Cloudflare reports for a request
// whose peer is a Cloudflare edge, or "" if peer is not Cloudflare or the
// header is missing / not an address.
func cloudflareClientIP(r *http.Request, peer string) string {
	if !isCloudflare(peer) {
		return ""
	}
	ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}
