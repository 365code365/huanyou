// Package ipclass tells whether an address is in mainland China, private, or
// elsewhere, using the CN list embedded for smart routing. It runs locally,
// so recording user IPs never sends them to a third-party lookup service.
package ipclass

import (
	"net/netip"
	"sort"
	"strings"
	"sync"

	"github.com/xtls/xray-core/app/router"
	"google.golang.org/protobuf/proto"

	"vpnplatform/server/internal/profile"
)

// Class of an address.
const (
	CN      = "cn"      // mainland China
	Private = "private" // LAN / loopback / CGNAT
	Abroad  = "abroad"  // everything else
	Unknown = ""        // not an IP
)

type span struct{ lo, hi netip.Addr }

type table struct{ cn, private []span }

var (
	once sync.Once
	tbl  table
)

func load() {
	var list router.GeoIPList
	if err := proto.Unmarshal(profile.CNGeoIPData(), &list); err != nil {
		return
	}
	for _, e := range list.Entry {
		var spans []span
		for _, c := range e.Cidr {
			a, ok := netip.AddrFromSlice(c.Ip)
			if !ok {
				continue
			}
			p, err := a.Unmap().Prefix(int(c.Prefix))
			if err != nil {
				continue
			}
			spans = append(spans, span{p.Masked().Addr(), last(p)})
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i].lo.Less(spans[j].lo) })
		switch strings.ToLower(e.CountryCode) {
		case "cn":
			tbl.cn = spans
		case "private":
			tbl.private = spans
		}
	}
}

// last returns the highest address of a prefix.
func last(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	bits := p.Bits()
	for i := range b {
		for bit := 0; bit < 8; bit++ {
			if i*8+bit >= bits {
				b[i] |= 0x80 >> bit
			}
		}
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

func in(spans []span, a netip.Addr) bool {
	i := sort.Search(len(spans), func(i int) bool { return a.Less(spans[i].lo) })
	// spans[i-1].lo <= a; ranges can be nested or overlap, so look back a little.
	for j := i - 1; j >= 0 && j >= i-8; j-- {
		if spans[j].lo.BitLen() == a.BitLen() && !a.Less(spans[j].lo) && !spans[j].hi.Less(a) {
			return true
		}
	}
	return false
}

// Of classifies ip.
func Of(ip string) string {
	once.Do(load)
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return Unknown
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || in(tbl.private, a) {
		return Private
	}
	if in(tbl.cn, a) {
		return CN
	}
	return Abroad
}
