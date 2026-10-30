package api

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The admin panel (../../../admin, Next.js + Ant Design) is statically
// exported by `npm run build` into adminui/dist and embedded into the binary,
// so the API stays a single file to deploy.
//
// Next's export writes one HTML file per route (users.html, nodes.html, …)
// and hashed assets under _next/static/.
//
//go:embed all:adminui/dist
var adminDist embed.FS

// mountAdminUI serves the panel at /admin/.
//
//	/admin/            -> index.html
//	/admin/users       -> users.html
//	/admin/_next/...   -> asset (long-lived cache; file names are content-hashed)
//	/admin/unknown     -> index.html (the console then routes client-side)
func mountAdminUI(mux *http.ServeMux) {
	dist, err := fs.Sub(adminDist, "adminui/dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(dist))

	serveHTML := func(w http.ResponseWriter, name string) bool {
		b, err := fs.ReadFile(dist, name)
		if err != nil {
			return false
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
		return true
	}

	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		if !serveHTML(w, "index.html") {
			http.Error(w, "admin UI not built: run `npm run build` in admin/", http.StatusServiceUnavailable)
		}
	})
	// Client-side navigation to the home route ("/" under basePath) requests
	// /admin.txt — the RSC payload of the root page — which lives at index.txt.
	mux.HandleFunc("GET /admin.txt", func(w http.ResponseWriter, r *http.Request) {
		b, err := fs.ReadFile(dist, "index.txt")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/x-component")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	})
	mux.HandleFunc("GET /admin/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.Trim(path.Clean("/"+strings.TrimPrefix(r.URL.Path, "/admin/")), "/")
		if p == "" {
			serveHTML(w, "index.html")
			return
		}
		// Exact file (assets, favicon, RSC payloads).
		if st, err := fs.Stat(dist, p); err == nil && !st.IsDir() {
			if strings.HasPrefix(p, "_next/static/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			r2 := *r
			r2.URL.Path = "/" + p
			files.ServeHTTP(w, &r2)
			return
		}
		// Route page: /admin/users -> users.html
		if serveHTML(w, p+".html") {
			return
		}
		// Unknown path (e.g. a stale bookmark): hand off to the app shell.
		serveHTML(w, "index.html")
	})
}
