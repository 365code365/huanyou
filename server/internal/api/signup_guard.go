package api

import (
	"net/netip"
	"sync"
	"time"
)

// signupGuard limits how many new device accounts one network creates per
// day. A device account is free to create (any random device id), so without
// a limit a script could mint free-trial accounts, or new "invitees" to farm
// referral days, without end.
//
// Two limits per IP (IPv6 per /64, which is what one phone or home rotates
// within), per UTC day:
//   - Guard.NewDevicesPerIPDay new accounts at all (default 30: generous, a
//     school or a carrier NAT shares one address);
//   - 3 of them may be credited to an invite code; later ones are created
//     without the referral bonus.
//
// In memory and bounded; a restart starts the day's counts afresh.
type signupGuard struct {
	mu   sync.Mutex
	day  int64
	nets map[netip.Prefix]*signupCount
}

type signupCount struct{ accounts, invites int }

const (
	signupInvitesPerIPDay = 3
	signupMaxNets         = 500_000
)

func signupNet(ip string) (netip.Prefix, bool) {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	bits := 32
	if a.Is6() {
		bits = 64
	}
	p, err := a.Prefix(bits)
	return p, err == nil
}

// allow records a new account from ip. ok=false: over the day's limit (no
// account is created). invite=false: the account may be created but must not
// be credited to an invite code.
func (g *signupGuard) allow(ip string, limit int, wantInvite bool, now time.Time) (ok, invite bool) {
	p, valid := signupNet(ip)
	if !valid || p.Addr().IsLoopback() {
		return true, wantInvite
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if d := now.Unix() / 86400; d != g.day || g.nets == nil {
		g.day, g.nets = d, map[netip.Prefix]*signupCount{}
	}
	c := g.nets[p]
	if c == nil {
		if len(g.nets) >= signupMaxNets {
			return true, false // under a huge spread, keep serving but credit nothing
		}
		c = &signupCount{}
		g.nets[p] = c
	}
	if c.accounts >= limit {
		return false, false
	}
	c.accounts++
	if wantInvite && c.invites < signupInvitesPerIPDay {
		c.invites++
		return true, true
	}
	return true, false
}
