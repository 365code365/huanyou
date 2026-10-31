package provision

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func signer(t *testing.T, k crypto.Signer) ssh.Signer {
	t.Helper()
	s, err := ssh.NewSignerFromSigner(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// sshServer accepts the password "right" and shows the given host keys.
func sshServer(t *testing.T, keys ...ssh.Signer) Target {
	t.Helper()
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if string(pass) == "right" {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	for _, k := range keys {
		cfg.AddHostKey(k)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					ch.Reject(ssh.Prohibited, "test")
				}
			}()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	p, _ := strconv.Atoi(port)
	return Target{Host: "127.0.0.1", Port: p, User: "root", Password: "right"}
}

func pin(k ssh.Signer) string {
	return k.PublicKey().Type() + " " + ssh.FingerprintSHA256(k.PublicKey())
}

func TestDialHostKeyPinning(t *testing.T) {
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	ed, ec, rs := signer(t, edKey), signer(t, ecKey), signer(t, rsaKey)
	ctx := context.Background()
	dial := func(tg Target) (string, error) {
		c, seen, err := tg.dial(ctx)
		if c != nil {
			c.Close()
		}
		return seen, err
	}

	all := sshServer(t, ec, rs, ed)
	// First contact: Ed25519 is asked for first (Go alone would take ECDSA).
	if seen, err := dial(all); err != nil || seen != pin(ed) {
		t.Fatalf("first contact: %q %v, want %q", seen, err, pin(ed))
	}
	// A pin of another type the server also has: exactly that key is shown.
	tg := all
	tg.HostKey = pin(ec)
	if seen, err := dial(tg); err != nil || seen != pin(ec) {
		t.Fatalf("ECDSA pin: %q %v", seen, err)
	}
	tg.HostKey = pin(rs)
	if seen, err := dial(tg); err != nil || seen != pin(rs) {
		t.Fatalf("RSA pin: %q %v", seen, err)
	}
	// Same type, other key (re-installed or someone in between): refused,
	// and the key shown is returned for an explicit re-trust.
	_, otherKey, _ := ed25519.GenerateKey(rand.Reader)
	tg.HostKey = pin(signer(t, otherKey))
	if seen, err := dial(tg); !errors.Is(err, ErrHostKey) || seen != pin(ed) {
		t.Fatalf("wrong key: %q %v", seen, err)
	}
	// The pinned type is gone from the server.
	edOnly := sshServer(t, ed)
	edOnly.HostKey = pin(ec)
	if seen, err := dial(edOnly); err == nil || seen != "" || !strings.Contains(err.Error(), "ecdsa-sha2-nistp256") {
		t.Fatalf("pinned type missing: %q %v", seen, err)
	}
	// Login refused: nothing is returned that could get pinned.
	bad := all
	bad.Password = "wrong"
	if seen, err := dial(bad); err == nil || seen != "" {
		t.Fatalf("failed login returned %q (%v)", seen, err)
	}
	// An older pin without the type still matches (Go's default choice).
	legacy := all
	legacy.HostKey = ssh.FingerprintSHA256(ec.PublicKey())
	if _, err := dial(legacy); err != nil {
		t.Fatalf("pin without type: %v", err)
	}
}
