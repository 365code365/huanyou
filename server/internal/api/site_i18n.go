package api

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"vpnplatform/server/internal/i18n"
)

// Languages of the public site (site_landing.html).
//
// Strings are keyed by their Simplified Chinese text, like the app's packs.
// A translation comes from, in order: the enabled language pack of the same
// code (admin「多语言」page), the built-in site_i18n.json, the Chinese key.
// scripts/i18n-extract.py adds the site keys to base.json so the admin's AI
// translation also covers the site when a new language is added.

//go:embed site_i18n.json
var siteI18nJSON []byte

var siteBuiltin = func() map[string]map[string]string {
	var m map[string]map[string]string
	if err := json.Unmarshal(siteI18nJSON, &m); err != nil {
		panic("site_i18n.json: " + err.Error())
	}
	return m
}()

// siteGoKeys are the site strings produced in Go rather than written in the
// template (picked up by scripts/i18n-extract.py).
var siteGoKeys = []string{
	"环游", "一键连接，畅游全球",
	"通用版", "64 位版", "32 位版（老款手机）", "x86 版（模拟器 / 平板）",
	"x64 版（Intel / AMD）", "ARM 版（骁龙等）",
	"通用版（Apple 芯片 + Intel）", "Apple 芯片版", "Intel 版",
}

const siteLangCookie = "site_lang"

// siteLang translates one page render. Methods are called from the template.
type siteLang struct {
	Code    string
	pack    map[string]string
	builtin map[string]string
}

func (l *siteLang) lookup(key string) string {
	if v := l.pack[key]; v != "" {
		return v
	}
	if v := l.builtin[key]; v != "" {
		return v
	}
	return key
}

var sitePlaceholder = regexp.MustCompile(`\{(\d+)\}`)

func fill(s string, arg func(i int) (string, bool)) string {
	return sitePlaceholder.ReplaceAllStringFunc(s, func(m string) string {
		i, _ := strconv.Atoi(m[1 : len(m)-1])
		if v, ok := arg(i); ok {
			return v
		}
		return m
	})
}

// T returns the translated text with {n} replaced by args (plain text; the
// template escapes it).
func (l *siteLang) T(key string, args ...any) string {
	return fill(l.lookup(key), func(i int) (string, bool) {
		if i < len(args) {
			return fmt.Sprint(args[i]), true
		}
		return "", false
	})
}

// TC is T with each argument shown as a <code> chip.
func (l *siteLang) TC(key string, args ...string) template.HTML {
	s := fill(html.EscapeString(l.lookup(key)), func(i int) (string, bool) {
		if i < len(args) {
			return "<code>" + html.EscapeString(args[i]) + "</code>", true
		}
		return "", false
	})
	return template.HTML(s)
}

type siteLangOption struct {
	Code, Name string
	RTL        bool
}

// siteLanguages lists 简体中文, then the enabled packs, then built-in-only languages.
func (s *Server) siteLanguages(ctx context.Context) []siteLangOption {
	out := []siteLangOption{{Code: "zh", Name: "简体中文"}}
	seen := map[string]bool{"zh": true}
	if packs, err := s.store.LangPacks(ctx, true); err == nil {
		for _, p := range packs {
			if !seen[p.Code] {
				seen[p.Code] = true
				out = append(out, siteLangOption{p.Code, p.NativeName, p.RTL})
			}
		}
	}
	var rest []string
	for code := range siteBuiltin {
		if !seen[code] {
			rest = append(rest, code)
		}
	}
	sort.Strings(rest)
	for _, code := range rest {
		opt := siteLangOption{Code: code, Name: code}
		for _, l := range i18n.Suggested {
			if l.Code == code {
				opt.Name, opt.RTL = l.NativeName, l.RTL
			}
		}
		out = append(out, opt)
	}
	return out
}

// pickSiteLang chooses ?lang=, then the cookie, then Accept-Language, then
// Chinese. A valid ?lang= is remembered in a cookie.
func pickSiteLang(w http.ResponseWriter, r *http.Request, opts []siteLangOption) string {
	has := func(c string) bool {
		for _, o := range opts {
			if o.Code == c {
				return true
			}
		}
		return false
	}
	if q := r.URL.Query().Get("lang"); q != "" && has(q) {
		http.SetCookie(w, &http.Cookie{Name: siteLangCookie, Value: q, Path: "/", MaxAge: 365 * 86400,
			HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
		return q
	}
	if c, err := r.Cookie(siteLangCookie); err == nil && has(c.Value) {
		return c.Value
	}
	return matchAcceptLanguage(r.Header.Get("Accept-Language"), has)
}

// matchAcceptLanguage maps browser tags onto the offered codes: exact match,
// then Chinese script/region (zh-TW / zh-HK / zh-Hant → zh-Hant, other zh →
// zh), then the base language (es-419 → es, pt → pt-BR).
func matchAcceptLanguage(header string, has func(string) bool) string {
	type tag struct {
		t string
		q float64
	}
	var tags []tag
	for _, part := range strings.Split(header, ",") {
		f := strings.Split(strings.TrimSpace(part), ";")
		t, q := strings.TrimSpace(f[0]), 1.0
		for _, p := range f[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
				q, _ = strconv.ParseFloat(v, 64)
			}
		}
		if t != "" && t != "*" && q > 0 {
			tags = append(tags, tag{t, q})
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].q > tags[j].q })
	for _, tg := range tags {
		lower := strings.ToLower(tg.t)
		base, _, _ := strings.Cut(lower, "-")
		if base == "zh" {
			if strings.Contains(lower, "hant") || strings.HasSuffix(lower, "-tw") || strings.HasSuffix(lower, "-hk") || strings.HasSuffix(lower, "-mo") {
				if has("zh-Hant") {
					return "zh-Hant"
				}
			}
			return "zh"
		}
		for _, c := range []string{tg.t, base} {
			if has(c) {
				return c
			}
		}
		if base == "pt" && has("pt-BR") {
			return "pt-BR"
		}
	}
	return "zh"
}

// siteTranslator loads the strings for code ("zh" needs none).
func (s *Server) siteTranslator(ctx context.Context, code string) *siteLang {
	l := &siteLang{Code: code, builtin: siteBuiltin[code]}
	if code != "zh" {
		if p, err := s.store.LangPack(ctx, code); err == nil && p.Enabled {
			l.pack = p.Strings
		}
	}
	return l
}

// siteJSKeys are the strings the page script needs: every I("…") call.
var siteJSKeys = func() []string {
	var out []string
	for _, m := range regexp.MustCompile(`\bI\("((?:[^"\\]|\\.)*)"\)`).FindAllStringSubmatch(landingHTML, -1) {
		out = append(out, m[1])
	}
	return out
}()

func (l *siteLang) jsStrings() map[string]string {
	m := make(map[string]string, len(siteJSKeys))
	for _, k := range siteJSKeys {
		m[k] = l.lookup(k)
	}
	return m
}

// Static files of the site (Earth textures: NASA Blue Marble and Black
// Marble 2012, public domain). Names are fixed, so they cache for a long time.
//
//go:embed site/*.jpg
var siteAssets embed.FS

func (s *Server) handleSiteAsset(w http.ResponseWriter, r *http.Request) {
	b, err := siteAssets.ReadFile("site/" + r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=2592000")
	w.Write(b)
}
