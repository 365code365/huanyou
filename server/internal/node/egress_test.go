package node

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"
)

type recordDialer struct{ got []string }

func (r *recordDialer) Dial(_ context.Context, _ xnet.Address, d xnet.Destination, _ *internet.SocketConfig) (net.Conn, error) {
	r.got = append(r.got, d.NetAddr())
	if strings.HasPrefix(d.NetAddr(), "198.51.100.1:") {
		return nil, errors.New("unreachable")
	}
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}
func (r *recordDialer) DestIpAddress() net.IP { return nil }

func TestEgressGuard(t *testing.T) {
	answers := map[string][]netip.Addr{
		"rebind.example": {netip.MustParseAddr("169.254.169.254")},
		"mixed.example":  {netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("198.51.100.1"), netip.MustParseAddr("2001:db8::1"), netip.MustParseAddr("203.0.113.7")},
	}
	lookupIP = func(_ context.Context, _, host string) ([]netip.Addr, error) { return answers[host], nil }
	defer func() { lookupIP = net.DefaultResolver.LookupNetIP }()
	rd := &recordDialer{}
	g := egressGuard{inner: rd}
	ctx := context.Background()

	if _, err := g.Dial(ctx, nil, xnet.TCPDestination(xnet.DomainAddress("rebind.example"), 80), nil); err == nil {
		t.Fatal("domain resolving to the metadata service was dialed")
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.5", "169.254.169.254", "::1", "fe80::1"} {
		if _, err := g.Dial(ctx, nil, xnet.TCPDestination(xnet.ParseAddress(ip), 22), nil); err == nil {
			t.Fatalf("%s was dialed", ip)
		}
	}
	c, err := g.Dial(ctx, nil, xnet.TCPDestination(xnet.DomainAddress("mixed.example"), 443), nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	// Private answer dropped, IPv4 tried first, the unreachable one skipped.
	want := []string{"198.51.100.1:443", "203.0.113.7:443"}
	if strings.Join(rd.got, ",") != strings.Join(want, ",") {
		t.Fatalf("dialed %v, want %v", rd.got, want)
	}
	if _, err := g.Dial(ctx, nil, xnet.TCPDestination(xnet.ParseAddress("8.8.8.8"), 53), nil); err != nil {
		t.Fatalf("public address refused: %v", err)
	}
}

type udpDialer struct{ pc net.PacketConn }

func (u udpDialer) Dial(_ context.Context, _ xnet.Address, d xnet.Destination, _ *internet.SocketConfig) (net.Conn, error) {
	dst, err := net.ResolveUDPAddr("udp", d.NetAddr())
	if err != nil {
		return nil, err
	}
	return &internet.PacketConnWrapper{PacketConn: u.pc, Dest: dst}, nil
}
func (udpDialer) DestIpAddress() net.IP { return nil }

type countingPC struct {
	net.PacketConn
	to []string
}

func (c *countingPC) WriteTo(p []byte, a net.Addr) (int, error) {
	c.to = append(c.to, a.String())
	return len(p), nil
}

// Full-cone UDP: every packet names its own target, so each one is checked.
func TestEgressGuardUDPPackets(t *testing.T) {
	pc := &countingPC{}
	g := egressGuard{inner: udpDialer{pc: pc}}
	c, err := g.Dial(context.Background(), nil, xnet.UDPDestination(xnet.ParseAddress("8.8.8.8"), 53), nil)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := c.(*internet.PacketConnWrapper)
	if !ok {
		t.Fatalf("freedom needs *internet.PacketConnWrapper, got %T", c)
	}
	for _, to := range []string{"127.0.0.53:53", "10.0.0.2:53", "169.254.169.254:80", "1.1.1.1:53"} {
		a, _ := net.ResolveUDPAddr("udp", to)
		if n, err := w.WriteTo([]byte("x"), a); n != 1 || err != nil {
			t.Fatalf("WriteTo %s: %d %v", to, n, err)
		}
	}
	if _, err := w.Write([]byte("x")); err != nil { // to the dialed target
		t.Fatal(err)
	}
	if got := strings.Join(pc.to, ","); got != "1.1.1.1:53,8.8.8.8:53" {
		t.Fatalf("sent to %s, want only the public targets", got)
	}
}
