package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

// SNI router for an API that shares :443 with a VPN node on the same server.
//
// Each connection's TLS ClientHello is read (not answered) to get the SNI.
// Connections for the API domain are handed to the HTTPS server; everything
// else (REALITY clients, probes, scanners) is relayed byte-for-byte to the
// node, prefixed with a PROXY protocol v1 header so the node sees the real
// client address. The node then behaves exactly as if it owned :443:
// unauthenticated connections are forwarded to its camouflage target.

var errSNIDone = errors.New("sni: hello read")

// peekSNI reads the ClientHello from c and returns its server name and the
// bytes consumed, which must be replayed to whoever handles the connection.
func peekSNI(c net.Conn) (string, []byte) {
	var buf bytes.Buffer
	var sni string
	_ = tls.Server(readOnlyConn{r: io.TeeReader(c, &buf)}, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			sni = h.ServerName
			return nil, errSNIDone
		},
	}).Handshake()
	return strings.ToLower(sni), buf.Bytes()
}

// readOnlyConn lets crypto/tls parse a ClientHello without writing anything.
type readOnlyConn struct{ r io.Reader }

func (c readOnlyConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (readOnlyConn) Write(p []byte) (int, error)        { return 0, io.ErrClosedPipe }
func (readOnlyConn) Close() error                       { return nil }
func (readOnlyConn) LocalAddr() net.Addr                { return nil }
func (readOnlyConn) RemoteAddr() net.Addr               { return nil }
func (readOnlyConn) SetDeadline(t time.Time) error      { return nil }
func (readOnlyConn) SetReadDeadline(t time.Time) error  { return nil }
func (readOnlyConn) SetWriteDeadline(t time.Time) error { return nil }

// prefixConn replays already-read bytes before reading from the connection.
type prefixConn struct {
	net.Conn
	r io.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// chanListener is a net.Listener fed by the router.
type chanListener struct {
	addr net.Addr
	ch   chan net.Conn
	once sync.Once
	done chan struct{}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *chanListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *chanListener) Addr() net.Addr { return l.addr }

// sniRouter accepts on ln, sends connections for any of domains (the API's
// domain and its aliases) to the returned listener and relays all others to
// passthrough (host:port of the node).
func sniRouter(ln net.Listener, domains []string, passthrough string) net.Listener {
	api := &chanListener{addr: ln.Addr(), ch: make(chan net.Conn), done: make(chan struct{})}
	ours := map[string]bool{}
	for _, d := range domains {
		ours[strings.ToLower(d)] = true
	}
	go func() {
		defer api.Close()
		for {
			c, err := ln.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				time.Sleep(50 * time.Millisecond)
				continue
			}
			go func() {
				_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
				sni, hello := peekSNI(c)
				_ = c.SetReadDeadline(time.Time{})
				if ours[sni] {
					select {
					case api.ch <- &prefixConn{Conn: c, r: io.MultiReader(bytes.NewReader(hello), c)}:
					case <-api.done:
						c.Close()
					}
					return
				}
				relay(c, hello, passthrough)
			}()
		}
	}()
	return api
}

// relay forwards c to the node with a PROXY v1 header and the replayed hello.
func relay(c net.Conn, hello []byte, backend string) {
	defer c.Close()
	b, err := net.DialTimeout("tcp", backend, 5*time.Second)
	if err != nil {
		slog.Warn("sni relay: node unreachable", "backend", backend, "err", err)
		return
	}
	defer b.Close()
	if _, err := b.Write(append([]byte(proxyHeader(c.RemoteAddr(), c.LocalAddr())), hello...)); err != nil {
		return
	}
	const (
		up   = 1 // client -> node
		down = 2 // node -> client
	)
	done := make(chan int, 2)
	pipe := func(dst, src net.Conn, dir int) {
		_, _ = io.Copy(dst, src) // TCP to TCP: io.Copy splices in the kernel
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- dir
	}
	go pipe(b, c, up)
	go pipe(c, b, down)
	// When one direction ends, the other must end too: a client that sends
	// a hello and then idles would otherwise hold two goroutines and two fds
	// forever (nothing reads with a deadline).
	if <-done == down {
		// The node finished with this connection: nothing the client still
		// sends can be delivered.
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	} else {
		// The client half-closed; the node ends the connection within
		// seconds itself (its uplinkOnly / downlinkOnly policy).
		_ = b.SetReadDeadline(time.Now().Add(2 * time.Minute))
	}
	<-done
}

func proxyHeader(src, dst net.Addr) string {
	s, okS := src.(*net.TCPAddr)
	d, okD := dst.(*net.TCPAddr)
	if !okS || !okD {
		return "PROXY UNKNOWN\r\n"
	}
	fam := "TCP4"
	sip, dip := s.IP.To4(), d.IP.To4()
	if sip == nil || dip == nil {
		fam, sip, dip = "TCP6", s.IP.To16(), d.IP.To16()
	}
	return fmt.Sprintf("PROXY %s %s %s %d %d\r\n", fam, sip, dip, s.Port, d.Port)
}
