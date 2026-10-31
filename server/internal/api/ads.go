package api

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"time"

	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/payment"
)

// ---- ads (public) ----

type adView struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Text  string `json:"text,omitempty"`
	Image string `json:"image"`
	Link  string `json:"link"`
}

func (s *Server) absURL(u string) string {
	if strings.HasPrefix(u, "/") {
		return s.cfg.PublicURL + u
	}
	return u
}

// handleAds: GET /api/v1/ads — what the app shows (both slots off by default).
func (s *Server) handleAds(w http.ResponseWriter, _ *http.Request) {
	a := s.cat().Ads
	now := time.Now()
	items := append([]config.Ad(nil), a.Items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Sort < items[j].Sort })
	home := []adView{}
	var popup *adView
	for _, ad := range items {
		if !ad.Live(now) {
			continue
		}
		v := adView{ID: ad.ID, Title: ad.Title, Text: ad.Text, Image: s.absURL(ad.Image), Link: ad.Link}
		switch {
		case ad.Slot == "home" && a.HomeEnabled:
			home = append(home, v)
		case ad.Slot == "popup" && a.PopupEnabled && popup == nil:
			popup = &v
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{
		"home":  map[string]any{"interval": a.HomeInterval, "items": home},
		"popup": map[string]any{"frequency": a.PopupFrequency, "item": popup},
	})
}

var adFile = regexp.MustCompile(`^[a-f0-9]{24}\.(png|jpg|jpeg|webp|gif)$`)

func (s *Server) adDir() string { return filepath.Join(s.cfg.DownloadDir, "ads") }

func (s *Server) handleAdImage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !adFile.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // random names never change
	http.ServeFile(w, r, filepath.Join(s.adDir(), name))
}

// adminAdUpload: multipart "file" (image ≤ 2 MB) → {"url": "/dl/ads/…"}.
func (s *Server) adminAdUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_file", "请选择 2 MB 以内的图片")
		return
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ext := map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp", "image/gif": "gif"}[http.DetectContentType(head[:n])]
	if ext == "" {
		writeErr(w, http.StatusBadRequest, "bad_file", "只支持 PNG / JPG / WebP / GIF 图片")
		return
	}
	if hdr.Size > 2<<20 {
		writeErr(w, http.StatusBadRequest, "too_large", "图片不能超过 2 MB")
		return
	}
	if err := os.MkdirAll(s.adDir(), 0o755); err != nil {
		s.internal(w, err)
		return
	}
	rnd := make([]byte, 12)
	_, _ = rand.Read(rnd)
	name := hex.EncodeToString(rnd) + "." + ext
	out, err := os.Create(filepath.Join(s.adDir(), name))
	if err != nil {
		s.internal(w, err)
		return
	}
	_, err = out.Write(head[:n])
	if err == nil {
		_, err = io.Copy(out, io.LimitReader(f, 2<<20))
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(out.Name())
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": "/dl/ads/" + name, "abs_url": s.cfg.PublicURL + "/dl/ads/" + name})
}

// ---- settings sections ----

// adminPutSection replaces one catalog section: payment | ads | guard.
func (s *Server) adminPutSection(w http.ResponseWriter, r *http.Request) {
	section := r.PathValue("section")
	var apply func(c *config.Catalog) error
	switch section {
	case "payment":
		var in config.PaymentConfig
		if !decode(w, r, &in) {
			return
		}
		apply = func(c *config.Catalog) error { c.Payment = in; return nil }
	case "ads":
		var in config.AdsConfig
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if !decodeBody(w, r, &in) {
			return
		}
		apply = func(c *config.Catalog) error { c.Ads = in; return nil }
	case "guard":
		var in config.GuardConfig
		if !decode(w, r, &in) {
			return
		}
		apply = func(c *config.Catalog) error { c.Guard = in; return nil }
	default:
		writeErr(w, http.StatusNotFound, "not_found", "设置项不存在")
		return
	}
	next, err := s.live.Update(apply, s.saveCatalog(r))
	if s.catalogErr(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"payment": next.Payment, "ads": next.Ads, "guard": next.Guard}[section])
}

// adminGatewayKeys generates a merchant id and keys (not saved until the admin saves).
func (s *Server) adminGatewayKeys(w http.ResponseWriter, _ *http.Request) {
	mid, sk, ek, err := payment.NewKeys()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"merchant_id": mid, "sign_key": sk, "enc_key": ek})
}

// adminGatewayTest builds a sample pay link for the current settings.
func (s *Server) adminGatewayTest(w http.ResponseWriter, _ *http.Request) {
	g := s.cat().Payment.Gateway
	gw := s.gateway()
	if gw == nil || g.PayLink == "" {
		writeErr(w, http.StatusBadRequest, "not_configured", "请先填写下单链接并生成密钥，然后保存")
		return
	}
	u, err := gw.PayURL(payment.Order{ID: "VPTEST" + time.Now().Format("20060102150405"), AmountCents: 100, Subject: "测试订单"},
		s.cfg.PublicURL+gatewayNotifyPath, s.cfg.PublicURL+"/pay/return?order=VPTEST")
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": u})
}

// adminGatewayTestQuery calls the payment center's query API for an order
// number (a real order, or any test number) and shows what came back, so the
// integration can be checked from the admin panel.
func (s *Server) adminGatewayTestQuery(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OrderID string `json:"order_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	gw := s.gateway()
	if gw == nil || gw.QueryURL == "" {
		writeErr(w, http.StatusBadRequest, "not_configured", "请先填写订单查询接口并保存")
		return
	}
	id := strings.TrimSpace(body.OrderID)
	if id == "" {
		id = "VPTEST" + time.Now().Format("20060102150405")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	o, err := gw.QueryOrder(ctx, id)
	ms := time.Since(start).Milliseconds()
	switch {
	case errors.Is(err, payment.ErrOrderUnknown):
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "found": false, "order_id": id, "ms": ms,
			"message": "支付中心返回 404 ORDER_NOT_FOUND（接口和密钥正常，只是没有这个订单）"})
	case err != nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "order_id": id, "ms": ms, "message": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "found": true, "order_id": id, "ms": ms, "order": o})
	}
}

//go:embed payment_doc.md
var paymentDoc string

var paymentDocTmpl = template.Must(template.New("doc").Parse(paymentDoc))

// adminPaymentDoc returns the integration guide (markdown) filled with this
// system's merchant id and endpoints. Keys are not included.
func (s *Server) adminPaymentDoc(w http.ResponseWriter, _ *http.Request) {
	g := s.cat().Payment.Gateway
	mid, link := g.MerchantID, g.PayLink
	if mid == "" {
		mid = "（尚未生成）"
	}
	if link == "" {
		link = "https://你的收银台/checkout"
	}
	queryURL := g.QueryURL
	if queryURL == "" {
		queryURL = "https://你的支付中心/api/order/query"
	}
	var b strings.Builder
	_ = paymentDocTmpl.Execute(&b, map[string]string{
		"MerchantID": mid, "PayLink": link,
		"NotifyURL": s.cfg.PublicURL + gatewayNotifyPath, "QueryURL": queryURL,
	})
	writeJSON(w, http.StatusOK, map[string]string{"markdown": b.String()})
}
