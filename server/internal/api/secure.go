package api

import (
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/maphash"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"vpnplatform/server/internal/store"
)

// App request security, on top of TLS (which the app pins to the CA roots).
//
// Session key: at sign-in the app sends an X25519 public key; the server
// answers with its own, and both derive a 32-byte master key with HKDF. The
// key never travels: the token carries it wrapped under the server's secret
// (auth.IssueSession), and the sign-in reply itself (token + account) is
// sealed with it. From master two keys are derived: one signs requests, one
// encrypts responses.
//
// Requests: every request with a session token carries
//
//	X-HY-TS     unix seconds (± 5 min of the server clock)
//	X-HY-Nonce  random, never reused (kept 10 min)
//	X-HY-Env    device integrity report (signing cert, debugger, hooks …)
//	X-HY-Sig    base64url HMAC-SHA256(sign key, canonical request)
//
// so a captured token alone cannot be used, a captured request cannot be
// replayed, and nothing in it (path, query, body, report) can be changed.
//
// Responses: successful bodies are AES-256-GCM sealed (gzipped first when
// large) and bound to the request nonce, served as application/octet-stream
// with X-HY-Enc. Error bodies stay plain JSON (codes and messages only).
//
// Apps from before this scheme (no session key) keep working until
// Guard.RequireSigned is switched on.

const (
	hdrTS    = "X-HY-TS"
	hdrNonce = "X-HY-Nonce"
	hdrSig   = "X-HY-Sig"
	hdrEnv   = "X-HY-Env"
	hdrEnc   = "X-HY-Enc"
	hdrTime  = "X-HY-Time"

	sigWindow  = 5 * time.Minute
	maxSigBody = 4 << 20
)

// subKeys derives the request-signing and the response-encryption key.
func subKeys(master []byte) (sign, enc []byte) {
	h := hmac.New(sha256.New, master)
	h.Write([]byte("hy-sign"))
	sign = h.Sum(nil)
	h = hmac.New(sha256.New, master)
	h.Write([]byte("hy-enc"))
	return sign, h.Sum(nil)
}

// keyExchange runs the server side of the X25519 exchange for the client's
// base64 public key: the server's public key, the master key, and the raw
// client key (used as associated data for the sealed sign-in reply).
func keyExchange(clientPubB64 string) (serverPub, master, clientPub []byte, err error) {
	clientPub, err = base64.StdEncoding.DecodeString(clientPubB64)
	if err != nil {
		return nil, nil, nil, err
	}
	pub, err := ecdh.X25519().NewPublicKey(clientPub)
	if err != nil {
		return nil, nil, nil, err
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	shared, err := priv.ECDH(pub) // fails for low-order points
	if err != nil {
		return nil, nil, nil, err
	}
	serverPub = priv.PublicKey().Bytes()
	salt := append(append([]byte{}, serverPub...), clientPub...)
	master, err = hkdf.Key(sha256.New, shared, salt, "huanyou-session-v1", 32)
	return serverPub, master, clientPub, err
}

// seal is AES-256-GCM: nonce (12) || ciphertext || tag (16).
func seal(key, aad, plain []byte) []byte {
	b, _ := aes.NewCipher(key)
	g, _ := cipher.NewGCM(b)
	nonce := make([]byte, g.NonceSize())
	_, _ = rand.Read(nonce)
	return g.Seal(nonce, nonce, plain, aad)
}

func unseal(key, aad, box []byte) ([]byte, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return nil, err
	}
	if len(box) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("short box")
	}
	return g.Open(nil, box[:g.NonceSize()], box[g.NonceSize():], aad)
}

// ---- request signatures ----

type secErr struct {
	status    int
	code, msg string
}

func (e *secErr) write(w http.ResponseWriter) {
	if e.code == "bad_time" {
		// The app corrects its clock offset from this and retries once.
		writeJSON(w, e.status, map[string]any{"error": e.code, "message": e.msg, "server_time": time.Now().Unix()})
		return
	}
	writeErr(w, e.status, e.code, e.msg)
}

// signedPath is the request path from /api/ on, so a reverse proxy that
// mounts the API under a prefix does not change what is signed.
func signedPath(r *http.Request) string {
	p := r.URL.EscapedPath()
	if i := strings.Index(p, "/api/"); i > 0 {
		p = p[i:]
	}
	return p
}

// canonicalRequest is what X-HY-Sig signs. The app builds the same string
// (lib/core/api.dart, ApiClient._sign).
func canonicalRequest(method, path, rawQuery, ts, nonce, env string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{"HY1", method, path, rawQuery, ts, nonce, env, hex.EncodeToString(sum[:])}, "\n"))
}

// checkSignature verifies the request signature and that the nonce is new.
// The body is read (and put back) to be covered by the signature.
func (s *Server) checkSignature(r *http.Request, uid int64, signKey []byte) (string, *secErr) {
	ts, nonce, sig := r.Header.Get(hdrTS), r.Header.Get(hdrNonce), r.Header.Get(hdrSig)
	if ts == "" || nonce == "" || sig == "" {
		return "", &secErr{http.StatusUnauthorized, "bad_signature", "请求签名缺失，请更新到最新版本"}
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	now := time.Now()
	if err != nil || now.Sub(time.Unix(t, 0)).Abs() > sigWindow {
		return "", &secErr{http.StatusUnauthorized, "bad_time", "设备时间不正确，请在系统设置中开启自动设置时间"}
	}
	if len(nonce) < 16 || len(nonce) > 64 {
		return "", &secErr{http.StatusUnauthorized, "bad_signature", "请求签名无效"}
	}
	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, maxSigBody+1))
		if err != nil || len(body) > maxSigBody {
			return "", &secErr{http.StatusRequestEntityTooLarge, "too_large", "请求过大"}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	want := hmac.New(sha256.New, signKey)
	want.Write(canonicalRequest(r.Method, signedPath(r), r.URL.RawQuery, ts, nonce, r.Header.Get(hdrEnv), body))
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want.Sum(nil)) {
		return "", &secErr{http.StatusUnauthorized, "bad_signature", "请求签名无效"}
	}
	// Recorded only after the signature checks out, so nobody without the
	// key can fill the cache.
	if s.nonces.seen(strconv.FormatInt(uid, 10)+":"+nonce, now.Unix()) {
		return "", &secErr{http.StatusUnauthorized, "replayed", "请求已失效，请重试"}
	}
	return nonce, nil
}

// nonceCache remembers request nonces long enough to refuse any replay a
// signature would still accept: a request is valid for sigWindow around its
// own timestamp, which is itself at most sigWindow from the server clock, so
// two generations of 2×sigWindow each are kept. Rotating generations replaces
// sweeping the whole map under the lock. Keys are 64-bit hashes (small; a
// collision only makes the app retry with a new nonce).
type nonceCache struct {
	mu        sync.Mutex
	seed      maphash.Seed
	cur, prev map[uint64]struct{}
	rotated   int64
	full      bool // cur reached nonceCacheMax (logged once per generation)
}

// nonceCacheMax bounds memory under a flood of signed requests: 2M per
// generation (~40 MB) holds a sustained ~3,300 signed requests a second.
// Beyond that, nonces are no longer recorded until the next rotation.
const nonceCacheMax = 2_000_000

func (c *nonceCache) seen(key string, now int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	gen := int64(2 * sigWindow / time.Second)
	if c.cur == nil {
		c.seed = maphash.MakeSeed()
		c.cur, c.prev, c.rotated = map[uint64]struct{}{}, map[uint64]struct{}{}, now
	}
	if now-c.rotated >= gen {
		c.prev, c.cur, c.rotated, c.full = c.cur, map[uint64]struct{}{}, now, false
	}
	h := maphash.String(c.seed, key)
	if _, ok := c.cur[h]; ok {
		return true
	}
	if _, ok := c.prev[h]; ok {
		return true
	}
	if len(c.cur) < nonceCacheMax {
		c.cur[h] = struct{}{}
	} else if !c.full {
		c.full = true
		slog.Warn("nonce cache full: replays are not detected until the next rotation", "entries", len(c.cur))
	}
	return false
}

// ---- device integrity ----

// parseEnv reads "p=android;sig=…;dbg=0;hook=0;root=0".
func parseEnv(v string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(v, ";") {
		if k, val, ok := strings.Cut(kv, "="); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(val)
		}
	}
	return m
}

// checkEnv applies the admin's integrity policy (Guard.AppSigners /
// BlockTampered) to the report the app signed into the request.
func (s *Server) checkEnv(r *http.Request, uid int64) *secErr {
	env := parseEnv(r.Header.Get(hdrEnv))
	cfg := s.cat().Guard
	if env["p"] == "android" && len(cfg.AppSigners) > 0 && !slices.Contains(cfg.AppSigners, strings.ToLower(env["sig"])) {
		s.logEnv(uid, r, "unofficial signer")
		return &secErr{http.StatusForbidden, "tampered_app", "检测到非官方安装包，请从官网下载正版应用"}
	}
	if env["dbg"] == "1" || env["hook"] == "1" {
		s.logEnv(uid, r, "debugger / hook detected")
		if cfg.BlockTampered {
			return &secErr{http.StatusForbidden, "unsafe_env", "检测到调试或注入工具，已停止服务。请关闭相关工具后重试"}
		}
	}
	return nil
}

// logEnv logs an integrity finding at most once an hour per user.
func (s *Server) logEnv(uid int64, r *http.Request, what string) {
	now := time.Now().Unix()
	if v, ok := s.envLogged.Load(uid); ok && now-v.(int64) < 3600 {
		return
	}
	s.envLogged.Store(uid, now)
	slog.Warn("app integrity", "user", uid, "finding", what, "env", r.Header.Get(hdrEnv), "ip", s.clientIP(r))
}

// ---- encrypted responses ----

// sealWriter buffers the handler's response and, if it is a success, sends
// it sealed with the session's encryption key.
type sealWriter struct {
	w      http.ResponseWriter
	key    []byte
	aad    []byte
	status int
	buf    bytes.Buffer
}

func (sw *sealWriter) Header() http.Header { return sw.w.Header() }

func (sw *sealWriter) WriteHeader(code int) {
	if sw.status == 0 {
		sw.status = code
	}
}

func (sw *sealWriter) Write(b []byte) (int, error) {
	if sw.status == 0 {
		sw.status = http.StatusOK
	}
	return sw.buf.Write(b)
}

func (sw *sealWriter) finish() {
	st := sw.status
	if st == 0 {
		st = http.StatusOK
	}
	h := sw.w.Header()
	body := sw.buf.Bytes()
	if st < 200 || st >= 300 {
		sw.w.WriteHeader(st)
		_, _ = sw.w.Write(body)
		return
	}
	if st == http.StatusNoContent {
		// A 204 can carry no body, so not the sealed one the app needs to
		// trust the answer: it read as a failure, and the app sent the same
		// analytics events again every round. An empty object, as a 200.
		st, body = http.StatusOK, []byte("{}")
	}
	zipped := h.Get("Content-Encoding") == "gzip"
	if !zipped && len(body) > 1400 {
		// Compress before encrypting: ciphertext does not compress.
		var zb bytes.Buffer
		gz := gzip.NewWriter(&zb)
		_, _ = gz.Write(body)
		_ = gz.Close()
		body, zipped = zb.Bytes(), true
	}
	h.Del("Content-Encoding")
	h.Del("Content-Length")
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Cache-Control", "no-store")
	if zipped {
		h.Set(hdrEnc, "1;z")
	} else {
		h.Set(hdrEnc, "1")
	}
	sw.w.WriteHeader(st)
	_, _ = sw.w.Write(seal(sw.key, sw.aad, body))
}

// secureSession checks a session-keyed request (signature, nonce,
// integrity) and returns the writer to answer through; call done() after
// the handler. For a token without a session key it returns w unchanged.
func (s *Server) secureSession(w http.ResponseWriter, r *http.Request, uid int64, key []byte) (http.ResponseWriter, func(), *secErr) {
	if key == nil {
		return w, func() {}, nil
	}
	signKey, encKey := subKeys(key)
	nonce, e := s.checkSignature(r, uid, signKey)
	if e != nil {
		return w, nil, e
	}
	if e := s.checkEnv(r, uid); e != nil {
		return w, nil, e
	}
	sw := &sealWriter{w: w, key: encKey, aad: []byte("resp:" + nonce)}
	return sw, sw.finish, nil
}

// optionalSession serves a public endpoint (plans, language packs, ads …)
// sealed when the app sends a valid session token; without one (first
// start, old app versions) it is served as before.
func (s *Server) optionalSession(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearer(r)
		if !ok {
			h(w, r)
			return
		}
		uid, key, err := s.issuer.VerifySession(tok)
		if err != nil || key == nil {
			h(w, r)
			return
		}
		sw, done, e := s.secureSession(w, r, uid, key)
		if e != nil {
			e.write(w)
			return
		}
		h(sw, r)
		done()
	}
}

// withServerTime tells the app the server clock on every response, so a
// phone with a wrong clock can still sign requests (X-HY-TS).
func withServerTime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(hdrTime, strconv.FormatInt(time.Now().Unix(), 10))
		next.ServeHTTP(w, r)
	})
}

// ---- sign-in with key exchange ----

// respondSession answers a sign-in that sent a public key: the token (with
// the session key inside) and the account, sealed for this client only.
func (s *Server) respondSession(w http.ResponseWriter, r *http.Request, u *store.User, clientPubB64 string) {
	serverPub, master, clientPub, err := keyExchange(clientPubB64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_key", "密钥协商失败，请重试")
		return
	}
	tok, exp, err := s.issuer.IssueSession(u.ID, master)
	if err != nil {
		s.internal(w, err)
		return
	}
	plain, err := json.Marshal(tokenResponse{Token: tok, ExpiresAt: exp, User: s.viewOf(r.Context(), u)})
	if err != nil {
		s.internal(w, err)
		return
	}
	_, encKey := subKeys(master)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{
		"v":          "1",
		"server_pub": base64.StdEncoding.EncodeToString(serverPub),
		"sealed":     base64.StdEncoding.EncodeToString(seal(encKey, append([]byte("auth:"), clientPub...), plain)),
	})
}

// handleRekey upgrades a token from before session keys (app updated from an
// older version, email accounts included) to a session token, once.
func (s *Server) handleRekey(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearer(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	uid, key, err := s.issuer.VerifySession(tok)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "登录已失效，请重新登录")
		return
	}
	if key != nil {
		writeErr(w, http.StatusConflict, "already_keyed", "已是安全会话")
		return
	}
	var body struct {
		Pub string `json:"pub"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Pub == "" {
		writeErr(w, http.StatusBadRequest, "bad_key", "缺少公钥")
		return
	}
	u, err := s.store.UserByID(r.Context(), uid)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "账号不存在")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	s.respondToken(w, r, u, body.Pub)
}
