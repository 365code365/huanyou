package api

import (
	"net"
	"net/http"
	"strings"

	"vpnplatform/server/internal/ipclass"
)

// When the API domain is proxied by Cloudflare (orange cloud), every request
// reaches us from a Cloudflare edge address, shared by many users. Rate
// limits, automatic bans, the new-device limit and IP records would then hit
// Cloudflare instead of the user: one scanner behind an edge would get the
// edge banned for everyone using it. With trust_cloudflare the real address
// is taken from CF-Connecting-IP, but only on connections that really come
// from Cloudflare's published ranges (ipclass.IsCloudflare), so a client
// cannot forge it.

// cloudflareClientIP is the visitor address Cloudflare reports for a request
// whose peer is a Cloudflare edge, or "" if peer is not Cloudflare or the
// header is missing / not an address.
func cloudflareClientIP(r *http.Request, peer string) string {
	if !ipclass.IsCloudflare(peer) {
		return ""
	}
	ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}
