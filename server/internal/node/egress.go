package node

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet"

	"vpnplatform/server/internal/profile"
)

// Egress guard: users must never reach this server's own loopback, LAN or
// link-local addresses (cloud metadata at 169.254.169.254 hands out instance
// credentials). The routing rule blocks IP targets and names Xray resolves
// for routing, but the freedom outbound ("AsIs") hands the name to the dial,
// which resolves it again: a rebinding DNS server answering a public address
// first and 127.0.0.1 / 169.254.169.254 the second time got through. Here the
// dial resolves once, drops private answers and connects only to an address
// it checked; UDP packets are checked one by one (guardPackets). (Xray's
// dialer controllers can't do this: their errors are only logged.)
type egressGuard struct{ inner internet.SystemDialer }

var privateNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, c := range profile.PrivateCIDRs {
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p)
		}
	}
	return out
}()

// privateDest reports whether users may not connect to a.
func privateDest(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsUnspecified() ||
		a.IsMulticast() || a.IsLinkLocalMulticast() {
		return true
	}
	for _, p := range privateNets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// guardEgress installs the guard in front of Xray's system dialer (once).
func guardEgress() {
	internet.UseAlternativeSystemDialer(egressGuard{inner: &internet.DefaultSystemDialer{}})
}

var lookupIP = net.DefaultResolver.LookupNetIP // tests replace it

func (g egressGuard) Dial(ctx context.Context, src xnet.Address, dest xnet.Destination, sockopt *internet.SocketConfig) (net.Conn, error) {
	switch {
	case dest.Address.Family().IsDomain():
		ips, err := lookupIP(ctx, "ip", dest.Address.Domain())
		if err != nil {
			return nil, err
		}
		ok := ips[:0]
		for _, a := range ips {
			if !privateDest(a) {
				ok = append(ok, a.Unmap())
			}
		}
		if len(ok) == 0 {
			return nil, fmt.Errorf("egress: %s resolves only to private addresses", dest.Address.Domain())
		}
		// IPv4 first: many VPS have broken IPv6 routing, and Go's own dialer
		// is not doing the fallback for us any more.
		slices.SortStableFunc(ok, func(a, b netip.Addr) int {
			switch {
			case a.Is4() && !b.Is4():
				return -1
			case !a.Is4() && b.Is4():
				return 1
			}
			return 0
		})
		var last error
		for i, a := range ok {
			d := dest
			d.Address = xnet.IPAddress(a.AsSlice())
			actx, cancel := ctx, context.CancelFunc(func() {})
			if i < len(ok)-1 {
				actx, cancel = context.WithTimeout(ctx, 4*time.Second) // leave time for the next address
			}
			c, err := g.inner.Dial(actx, src, d, sockopt)
			cancel()
			if err == nil {
				return guardPackets(c), nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, last
	case dest.Address.Family().IsIP():
		if a, ok := netip.AddrFromSlice(dest.Address.IP()); ok && privateDest(a) {
			return nil, fmt.Errorf("egress: %s is a private address", a)
		}
	}
	c, err := g.inner.Dial(ctx, src, dest, sockopt)
	if err != nil {
		return nil, err
	}
	return guardPackets(c), nil
}

// guardPackets applies the check to every UDP packet: freedom sends each one
// with WriteTo to the address it carries (full cone; XUDP packets each name
// their own target), and only the first target went through Dial.
func guardPackets(c net.Conn) net.Conn {
	if pc, ok := c.(*internet.PacketConnWrapper); ok {
		pc.PacketConn = guardedPacketConn{pc.PacketConn}
	}
	return c
}

type guardedPacketConn struct{ net.PacketConn }

func (g guardedPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if ua, ok := addr.(*net.UDPAddr); ok {
		if a, ok := netip.AddrFromSlice(ua.IP); ok && privateDest(a) {
			return len(p), nil // dropped, as a firewall would
		}
	}
	return g.PacketConn.WriteTo(p, addr)
}

func (g egressGuard) DestIpAddress() net.IP { return g.inner.DestIpAddress() }
