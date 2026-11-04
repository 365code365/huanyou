package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

func selfSigned(t *testing.T, host string) tls.Certificate {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func TestSNIRouter(t *testing.T) {
	// Fake node: expects a PROXY v1 line, then a TLS ClientHello it terminates.
	node, _ := net.Listen("tcp", "127.0.0.1:0")
	defer node.Close()
	gotHeader := make(chan string, 1)
	nodeCert := selfSigned(t, "www.apple.com")
	go func() {
		for {
			c, err := node.Accept()
			if err != nil {
				return
			}
			go func() {
				br := bufio.NewReader(c)
				line, _ := br.ReadString('\n')
				gotHeader <- line
				tc := tls.Server(&prefixConn{Conn: c, r: br}, &tls.Config{Certificates: []tls.Certificate{nodeCert}})
				_, _ = io.WriteString(tc, "node\n")
				tc.Close()
			}()
		}
	}()

	front, _ := net.Listen("tcp", "127.0.0.1:0")
	defer front.Close()
	apiLn := sniRouter(front, []string{"api.example.com", "old.example.com"}, node.Addr().String())
	apiCert := selfSigned(t, "api.example.com")
	go func() {
		for {
			c, err := apiLn.Accept()
			if err != nil {
				return
			}
			go func() {
				tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{apiCert}})
				_, _ = io.WriteString(tc, "api\n")
				tc.Close()
			}()
		}
	}()

	dial := func(sni string) string {
		c, err := tls.Dial("tcp", front.Addr().String(), &tls.Config{ServerName: sni, InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("%s: %v", sni, err)
		}
		defer c.Close()
		if got := c.ConnectionState().PeerCertificates[0].Subject.CommonName; got != sni {
			t.Fatalf("%s: served cert for %s", sni, got)
		}
		line, _ := bufio.NewReader(c).ReadString('\n')
		return strings.TrimSpace(line)
	}
	if got := dial("api.example.com"); got != "api" {
		t.Fatalf("api domain went to %q", got)
	}
	if got := dial("www.apple.com"); got != "node" {
		t.Fatalf("other SNI went to %q", got)
	}
	h := <-gotHeader
	if !strings.HasPrefix(h, "PROXY TCP4 127.0.0.1 127.0.0.1 ") {
		t.Fatalf("bad PROXY header %q", h)
	}
}
