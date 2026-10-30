package api

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The public download site: GET / , /download and /i/{code} (invite).
// It lists the latest uploaded build per platform and is clearly labelled as
// an internal test app, not open to the public.

type siteBuild struct {
	Version string
	Build   int64
	Date    string
	Size    string
	Notes   []string
	URL     string
	Label   string        // CPU build of URL when there are several, e.g. "x64 版（Intel / AMD）"
	Others  []siteVariant // other CPU builds of the same version
}

type siteVariant struct {
	Label, Size, URL string
}

// latestBuild resolves what the site offers for a platform, in priority:
// the latest published release, the share-settings link, the legacy APK.
// A build published for several CPUs shows the first ABI of the platform
// spec as the main button and the others as extra links.
func (s *Server) latestBuild(ctx context.Context, platform string) *siteBuild {
	spec := platforms[platform]
	if all, err := s.store.LatestBuilds(ctx, platform); err == nil && len(all) > 0 {
		sort.SliceStable(all, func(i, j int) bool { return spec.abiIndex(all[i].ABI) < spec.abiIndex(all[j].ABI) })
		r := all[0]
		b := &siteBuild{Version: r.Version, Build: r.Build, Date: r.CreatedAt.Format("2006-01-02"), URL: s.releaseURL(r), Size: sizeLabel(r.Size)}
		if len(all) > 1 { // name the main download too when there is a choice
			b.Label = spec.ABIs[max(0, spec.abiIndex(r.ABI))].Label
		}
		for _, x := range all[1:] {
			label := x.ABI
			if i := spec.abiIndex(x.ABI); i >= 0 {
				label = spec.ABIs[i].Label
			}
			b.Others = append(b.Others, siteVariant{Label: label, URL: s.releaseURL(x), Size: sizeLabel(x.Size)})
		}
		for _, l := range strings.Split(r.Notes, "\n") {
			if l = strings.TrimSpace(strings.TrimLeft(l, "-•*· ")); l != "" {
				b.Notes = append(b.Notes, l)
			}
		}
		return b
	}
	share := s.cat().Share
	if platform == "android" {
		if share.AndroidURL != "" {
			return &siteBuild{URL: share.AndroidURL}
		}
		if st, err := os.Stat(filepath.Join(s.cfg.DownloadDir, "app.apk")); err == nil && !st.IsDir() {
			return &siteBuild{URL: "/dl/app.apk", Size: humanBytes(st.Size()), Date: st.ModTime().Format("2006-01-02")}
		}
	}
	if platform == "ios" && share.IOSURL != "" {
		return &siteBuild{URL: share.IOSURL}
	}
	return nil
}

// androidURL is the link the invite share text and older code paths use.
func (s *Server) androidURL() string {
	if b := s.latestBuild(context.Background(), "android"); b != nil {
		return b.URL
	}
	return ""
}

func sizeLabel(n int64) string {
	if n <= 0 {
		return ""
	}
	return humanBytes(n)
}

// humanBytes formats a size like "116.9 MB".
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%d KB", n>>10)
	}
}

var codePattern = regexp.MustCompile(`^[A-Z2-9]{7}$`)

func (s *Server) handleLanding(w http.ResponseWriter, r *http.Request) {
	code := strings.ToUpper(r.PathValue("code"))
	if !codePattern.MatchString(code) {
		code = ""
	}
	ref := s.cat().Referral
	slogan := s.cat().Share.Slogan
	if slogan == "" {
		slogan = "一键连接，畅游全球"
	}
	langs := s.siteLanguages(r.Context())
	lang := pickSiteLang(w, r, langs)
	tr := s.siteTranslator(r.Context(), lang)
	cur := langs[0]
	for _, o := range langs {
		if o.Code == lang {
			cur = o
		}
	}
	htmlLang := lang
	if lang == "zh" {
		htmlLang = "zh-CN"
	}
	data := map[string]any{
		// App stays Chinese: the copied invite text must match the app's 环游邀请码 pattern.
		"App": AppName, "Brand": tr.T(AppName), "Slogan": tr.T(slogan), "Code": code,
		"L": tr, "Langs": langs, "Lang": cur, "HTMLLang": htmlLang, "Path": r.URL.Path, "JSI": tr.jsStrings(),
		"Android": s.latestBuild(r.Context(), "android"), "IOS": s.latestBuild(r.Context(), "ios"),
		"Windows":     s.latestBuild(r.Context(), "windows"),
		"Mac":         s.latestBuild(r.Context(), "macos"),
		"InviteeDays": 0, "Year": time.Now().Year(),
	}
	if ref.Enabled && code != "" {
		data["InviteeDays"] = ref.InviteeDays
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept-Language, Cookie")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow") // internal test app: keep it out of search engines
	if err := landingTmpl.Execute(w, data); err != nil {
		slog.Warn("landing render", "err", err)
	}
}

// handleRobots keeps the internal test site out of search engines.
func (s *Server) handleRobots(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

// site_landing.html is the public intro + download page (encryption and
// fingerprint-erasure sections, then the per-platform download cards).
//
//go:embed site_landing.html
var landingHTML string

var landingTmpl = template.Must(template.New("landing").Parse(landingHTML))
