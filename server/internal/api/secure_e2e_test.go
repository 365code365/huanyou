package api

import (
	"bytes"
	"compress/gzip"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"vpnplatform/server/internal/config"
)

// secClient speaks the app's request-security protocol (the Dart side is
// lib/core/api.dart), so the server half can be tested end to end.
type secClient struct {
	e        *iapEnv
	token    string
	sign     []byte
	enc      []byte
	env      string
	clockOff int64 // seconds added to the local clock when signing
}

// signIn does the X25519 sign-in at path (device / rekey) and unseals the reply.
func (e *iapEnv) signIn(path, bearer string, body map[string]string) *secClient {
	e.t.Helper()
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	pub := priv.PublicKey().Bytes()
	body["pub"] = base64.StdEncoding.EncodeToString(pub)
	m := e.call("POST", path, bearer, body, 200, nil)
	serverPub, _ := m["server_pub"].(string)
	sealed, _ := m["sealed"].(string)
	if _, plain := m["token"]; plain || sealed == "" {
		e.t.Fatalf("sign-in reply not sealed: %v", m)
	}
	sp, _ := base64.StdEncoding.DecodeString(serverPub)
	spk, err := ecdh.X25519().NewPublicKey(sp)
	if err != nil {
		e.t.Fatal(err)
	}
	shared, _ := priv.ECDH(spk)
	master, _ := hkdf.Key(sha256.New, shared, append(append([]byte{}, sp...), pub...), "huanyou-session-v1", 32)
	sign, enc := subKeys(master)
	box, _ := base64.StdEncoding.DecodeString(sealed)
	plain, err := unseal(enc, append([]byte("auth:"), pub...), box)
	if err != nil {
		e.t.Fatalf("unseal sign-in: %v", err)
	}
	var tr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(plain, &tr)
	if tr.Token == "" {
		e.t.Fatalf("no token in sealed reply: %s", plain)
	}
	return &secClient{e: e, token: tr.Token, sign: sign, enc: enc, env: "p=android;sig=aa;dbg=0;hook=0;root=0"}
}

type secReq struct {
	method, path string
	body         any
	nonce, ts    string // override (replay / clock tests)
	tamper       func(*http.Request)
}

// do sends a signed request; returns status, decrypted body (or the plain error body) and the nonce used.
func (c *secClient) do(q secReq) (int, []byte, string) {
	c.e.t.Helper()
	var raw []byte
	if q.body != nil {
		raw, _ = json.Marshal(q.body)
	}
	req, _ := http.NewRequest(q.method, c.e.api.URL+q.path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	ts := q.ts
	if ts == "" {
		ts = strconv.FormatInt(time.Now().Unix()+c.clockOff, 10)
	}
	nonce := q.nonce
	if nonce == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		nonce = hex.EncodeToString(b)
	}
	h := hmac.New(sha256.New, c.sign)
	h.Write(canonicalRequest(q.method, req.URL.EscapedPath(), req.URL.RawQuery, ts, nonce, c.env, raw))
	req.Header.Set(hdrTS, ts)
	req.Header.Set(hdrNonce, nonce)
	req.Header.Set(hdrEnv, c.env)
	req.Header.Set(hdrSig, base64.RawURLEncoding.EncodeToString(h.Sum(nil)))
	if q.tamper != nil {
		q.tamper(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if flag := resp.Header.Get(hdrEnc); flag != "" {
		plain, err := unseal(c.enc, []byte("resp:"+nonce), out)
		if err != nil {
			c.e.t.Fatalf("unseal %s: %v", q.path, err)
		}
		if strings.Contains(flag, "z") {
			zr, err := gzip.NewReader(bytes.NewReader(plain))
			if err != nil {
				c.e.t.Fatal(err)
			}
			plain, _ = io.ReadAll(zr)
		}
		out = plain
	}
	return resp.StatusCode, out, nonce
}

func (e *iapEnv) setGuard(fn func(*config.GuardConfig)) {
	e.t.Helper()
	if _, err := e.srv.live.Update(func(c *config.Catalog) error { fn(&c.Guard); return nil }, func(*config.Catalog) error { return nil }); err != nil {
		e.t.Fatal(err)
	}
}

func errCode(b []byte) string {
	var m struct{ Error string }
	_ = json.Unmarshal(b, &m)
	return m.Error
}

func TestRequestSecurity(t *testing.T) {
	e := newIAPEnv(t, 0)
	c := e.signIn("/api/v1/auth/device", "", map[string]string{"device_id": "sec-device-0001", "platform": "android"})

	t.Run("signed request is answered sealed", func(t *testing.T) {
		st, body, _ := c.do(secReq{method: "GET", path: "/api/v1/me"})
		if st != 200 || !strings.Contains(string(body), "recharge_code") {
			t.Fatalf("me: %d %s", st, body)
		}
	})

	t.Run("wire body is ciphertext", func(t *testing.T) {
		// A signed request sent by hand, to look at what is on the wire.
		req, _ := http.NewRequest("GET", e.api.URL+"/api/v1/me", nil)
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		n := "0123456789abcdef0123"
		h := hmac.New(sha256.New, c.sign)
		h.Write(canonicalRequest("GET", "/api/v1/me", "", ts, n, c.env, nil))
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set(hdrTS, ts)
		req.Header.Set(hdrNonce, n)
		req.Header.Set(hdrEnv, c.env)
		req.Header.Set(hdrSig, base64.RawURLEncoding.EncodeToString(h.Sum(nil)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		wire, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Header.Get("Content-Type") != "application/octet-stream" || json.Valid(wire) || bytes.Contains(wire, []byte("VP-")) {
			t.Fatalf("response not encrypted: %q", wire)
		}
	})

	t.Run("replayed nonce is refused", func(t *testing.T) {
		st, _, nonce := c.do(secReq{method: "GET", path: "/api/v1/me"})
		if st != 200 {
			t.Fatalf("first %d", st)
		}
		st, body, _ := c.do(secReq{method: "GET", path: "/api/v1/me", nonce: nonce})
		if st != 401 || errCode(body) != "replayed" {
			t.Fatalf("replay: %d %s", st, body)
		}
	})

	t.Run("token without the key cannot sign", func(t *testing.T) {
		thief := *c
		thief.sign = bytes.Repeat([]byte{7}, 32)
		st, body, _ := thief.do(secReq{method: "GET", path: "/api/v1/me"})
		if st != 401 || errCode(body) != "bad_signature" {
			t.Fatalf("forged: %d %s", st, body)
		}
		// Unsigned use of the stolen token.
		e.call("GET", "/api/v1/me", c.token, nil, 401, nil)
	})

	t.Run("changed query / body / report is refused", func(t *testing.T) {
		st, _, _ := c.do(secReq{method: "GET", path: "/api/v1/profile?mode=direct", tamper: func(r *http.Request) { r.URL.RawQuery = "mode=ai" }})
		if st != 401 {
			t.Fatalf("query tamper: %d", st)
		}
		st, _, _ = c.do(secReq{method: "POST", path: "/api/v1/referral/bind", body: map[string]string{"code": "AAAAAAA"},
			tamper: func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(`{"code":"BBBBBBB"}`)) }})
		if st != 401 {
			t.Fatalf("body tamper: %d", st)
		}
		st, _, _ = c.do(secReq{method: "GET", path: "/api/v1/me", tamper: func(r *http.Request) { r.Header.Set(hdrEnv, "p=android;hook=0") }})
		if st != 401 {
			t.Fatalf("env tamper: %d", st)
		}
	})

	t.Run("wrong clock gets server time, then works with the offset", func(t *testing.T) {
		skewed := *c
		skewed.clockOff = -3600
		st, body, _ := skewed.do(secReq{method: "GET", path: "/api/v1/me"})
		if st != 401 || errCode(body) != "bad_time" {
			t.Fatalf("skew: %d %s", st, body)
		}
		var m struct {
			ServerTime int64 `json:"server_time"`
		}
		_ = json.Unmarshal(body, &m)
		skewed.clockOff += m.ServerTime - (time.Now().Unix() - 3600)
		if st, body, _ := skewed.do(secReq{method: "GET", path: "/api/v1/me"}); st != 200 {
			t.Fatalf("after offset: %d %s", st, body)
		}
	})

	t.Run("large profile-sized body is gzipped inside the seal", func(t *testing.T) {
		st, body, _ := c.do(secReq{method: "GET", path: "/api/v1/plans?platform=android&build=1"})
		if st != 200 || !json.Valid(body) {
			t.Fatalf("plans: %d %s", st, body)
		}
	})

	t.Run("public endpoint stays open without a token", func(t *testing.T) {
		m := e.call("GET", "/api/v1/plans?platform=android&build=1", "", nil, 200, nil)
		if m["plans"] == nil {
			t.Fatalf("plans: %v", m)
		}
	})

	t.Run("legacy token works until required, then upgrades via rekey", func(t *testing.T) {
		e.call("GET", "/api/v1/me", e.tok, nil, 200, nil)
		e.setGuard(func(g *config.GuardConfig) { g.RequireSigned = true })
		defer e.setGuard(func(g *config.GuardConfig) { g.RequireSigned = false })
		m := e.call("GET", "/api/v1/me", e.tok, nil, 401, nil)
		if m["error"] != "key_required" {
			t.Fatalf("legacy: %v", m)
		}
		e.call("POST", "/api/v1/auth/device", "", map[string]string{"device_id": "old-app-device-01", "platform": "android"}, 426, nil)
		up := e.signIn("/api/v1/auth/rekey", e.tok, map[string]string{})
		if st, body, _ := up.do(secReq{method: "GET", path: "/api/v1/me"}); st != 200 {
			t.Fatalf("after rekey: %d %s", st, body)
		}
		// A session token cannot be re-keyed (that would let a thief mint a key).
		priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
		e.call("POST", "/api/v1/auth/rekey", up.token, map[string]string{"pub": base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())}, 409, nil)
	})

	t.Run("integrity policy", func(t *testing.T) {
		e.setGuard(func(g *config.GuardConfig) { g.AppSigners, g.BlockTampered = []string{strings.Repeat("ab", 32)}, true })
		defer e.setGuard(func(g *config.GuardConfig) { g.AppSigners, g.BlockTampered = nil, false })
		st, body, _ := c.do(secReq{method: "GET", path: "/api/v1/me"})
		if st != 403 || errCode(body) != "tampered_app" {
			t.Fatalf("unofficial signer: %d %s", st, body)
		}
		official := *c
		official.env = "p=android;sig=" + strings.Repeat("ab", 32) + ";dbg=0;hook=0;root=1"
		if st, body, _ := official.do(secReq{method: "GET", path: "/api/v1/me"}); st != 200 {
			t.Fatalf("official (rooted is fine): %d %s", st, body)
		}
		official.env = "p=android;sig=" + strings.Repeat("ab", 32) + ";dbg=0;hook=1;root=0"
		if st, body, _ := official.do(secReq{method: "GET", path: "/api/v1/me"}); st != 403 || errCode(body) != "unsafe_env" {
			t.Fatalf("hooked: %d %s", st, body)
		}
	})
}
