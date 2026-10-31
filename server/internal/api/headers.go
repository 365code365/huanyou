package api

import (
	"net/http"
	"strings"
)

// withSecurityHeaders sets the headers every response should carry:
//   - nosniff: a download or JSON body is never run as HTML / script;
//   - a referrer policy that sends only the origin to other sites;
//   - the admin panel can't be framed (clickjacking) and loads no plugins;
//   - HSTS when this process terminates TLS itself, so the panel can't be
//     opened over plain http after the first visit.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if strings.HasPrefix(r.URL.Path, "/admin") {
			h.Set("X-Frame-Options", "DENY")
			h.Set("Content-Security-Policy", "frame-ancestors 'none'; object-src 'none'; base-uri 'self'")
		}
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}
