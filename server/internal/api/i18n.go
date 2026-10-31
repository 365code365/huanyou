package api

import (
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"vpnplatform/server/internal/i18n"
	"vpnplatform/server/internal/store"
)

const aiSettingKey = "ai_translate"

// ---- app ----

type langView struct {
	Code       string `json:"code"`
	Name       string `json:"name"`
	NativeName string `json:"native_name"`
	RTL        bool   `json:"rtl"`
	Version    int64  `json:"version"`
}

// handleLanguages lists enabled packs. Public: the app asks before login.
func (s *Server) handleLanguages(w http.ResponseWriter, r *http.Request) {
	packs, err := s.store.LangPacks(r.Context(), true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", "服务器内部错误")
		return
	}
	out := make([]langView, 0, len(packs))
	for _, p := range packs {
		out = append(out, langView{p.Code, p.Name, p.NativeName, p.RTL, p.Version})
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{"languages": out})
}

func (s *Server) handleLanguagePack(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.LangPack(r.Context(), r.PathValue("code"))
	if err != nil || !p.Enabled {
		writeErr(w, http.StatusNotFound, "not_found", "语言包不存在")
		return
	}
	etag := `"` + p.Code + "-" + strconv.FormatInt(p.Version, 10) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	strs := make(map[string]string, len(p.Strings))
	for k, v := range p.Strings {
		if v != "" {
			strs[k] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": p.Code, "version": p.Version, "rtl": p.RTL, "strings": strs})
}

// ---- admin ----

func (s *Server) aiConfig(r *http.Request) i18n.AIConfig {
	var c i18n.AIConfig
	_, _ = s.store.Setting(r.Context(), aiSettingKey, &c)
	return c
}

func maskKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("•", len(k))
	}
	return k[:3] + "…" + k[len(k)-4:]
}

func (s *Server) adminListLanguages(w http.ResponseWriter, r *http.Request) {
	packs, err := s.store.LangPacks(r.Context(), false)
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	if packs == nil {
		packs = []*store.LangPack{}
	}
	ai := s.aiConfig(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"languages":  packs,
		"base_count": len(i18n.Base()),
		"suggested":  i18n.Suggested,
		"ai":         map[string]any{"ready": ai.Ready(), "base_url": ai.BaseURL, "model": ai.Model, "api_key": maskKey(ai.APIKey)},
	})
}

// adminI18nBase is the translation template: every Chinese source string.
func (s *Server) adminI18nBase(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"keys":   i18n.Base(),
		"prompt": i18n.Prompt(r.URL.Query().Get("name"), r.URL.Query().Get("code")),
	})
}

func (s *Server) adminGetLanguage(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.LangPack(r.Context(), r.PathValue("code"))
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) adminPutLanguage(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !i18n.ValidCode(code) {
		writeErr(w, http.StatusBadRequest, "bad_code", "语言代码格式不正确，例如 en、es、pt-BR")
		return
	}
	var in struct {
		Name       string            `json:"name"`
		NativeName string            `json:"native_name"`
		RTL        bool              `json:"rtl"`
		Enabled    bool              `json:"enabled"`
		Strings    map[string]string `json:"strings"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if !decodeBody(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.NativeName) == "" {
		writeErr(w, http.StatusBadRequest, "bad_name", "请填写语言的本地名称，例如 English")
		return
	}
	strs := make(map[string]string, len(in.Strings))
	for k, v := range in.Strings {
		if k = strings.TrimSpace(k); k != "" {
			strs[k] = v
		}
	}
	p, err := s.store.SaveLangPack(r.Context(), &store.LangPack{
		Code: code, Name: strings.TrimSpace(in.Name), NativeName: strings.TrimSpace(in.NativeName),
		RTL: in.RTL, Enabled: in.Enabled, Strings: strs,
	})
	if err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"language": p, "placeholder_mismatch": i18n.PlaceholderMismatches(strs)})
}

func (s *Server) adminDeleteLanguage(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteLangPack(r.Context(), r.PathValue("code")); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminParsePack validates pasted AI output: tolerant JSON parsing plus a
// coverage / placeholder report, without saving.
func (s *Server) adminParsePack(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if !decodeBody(w, r, &in) {
		return
	}
	m, err := i18n.ParsePack(in.Text)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"strings": m, "placeholder_mismatch": i18n.PlaceholderMismatches(m)})
}

// adminTranslate translates one batch (the panel loops over batches so each
// request stays well under proxy timeouts and shows progress).
func (s *Server) adminTranslate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string   `json:"code"`
		Name string   `json:"name"`
		Keys []string `json:"keys"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if !decodeBody(w, r, &in) {
		return
	}
	if len(in.Keys) == 0 || len(in.Keys) > 120 {
		writeErr(w, http.StatusBadRequest, "bad_keys", "每批 1–120 条")
		return
	}
	cfg := s.aiConfig(r)
	if !cfg.Ready() {
		writeErr(w, http.StatusBadRequest, "ai_not_configured", "未配置 AI 翻译接口，请先在本页填写接口地址、密钥和模型")
		return
	}
	m, err := i18n.Translate(r.Context(), cfg, in.Name, in.Code, in.Keys)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "ai_failed", err.Error())
		return
	}
	// Only keep keys we asked for.
	out := make(map[string]string, len(in.Keys))
	for _, k := range in.Keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"strings": out})
}

func (s *Server) adminPutAIConfig(w http.ResponseWriter, r *http.Request) {
	var in i18n.AIConfig
	if !decode(w, r, &in) {
		return
	}
	cur := s.aiConfig(r)
	in.BaseURL, in.Model = strings.TrimSpace(in.BaseURL), strings.TrimSpace(in.Model)
	in.APIKey = strings.TrimSpace(in.APIKey)
	if in.APIKey == "" || strings.Contains(in.APIKey, "…") || strings.Contains(in.APIKey, "•") {
		in.APIKey = cur.APIKey // unchanged (the panel only sees the masked key)
	}
	if in.BaseURL != "" && !aiURLAllowed(in.BaseURL) {
		writeErr(w, http.StatusBadRequest, "bad_url", "接口地址需以 https:// 开头（http:// 只允许本机或内网地址）")
		return
	}
	if err := s.store.PutSetting(r.Context(), aiSettingKey, in); err != nil {
		s.adminStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": in.Ready(), "base_url": in.BaseURL, "model": in.Model, "api_key": maskKey(in.APIKey)})
}

// aiURLAllowed: https anywhere; plain http only to loopback / private hosts
// (a self-hosted model), since the API key travels with every request.
func aiURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		h := u.Hostname()
		if h == "localhost" {
			return true
		}
		a, err := netip.ParseAddr(h)
		return err == nil && (a.IsLoopback() || a.IsPrivate())
	}
	return false
}
