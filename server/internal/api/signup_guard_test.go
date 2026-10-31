package api

import (
	"testing"
	"time"
)

func TestSignupGuard(t *testing.T) {
	var g signupGuard
	now := time.Unix(1_800_000_000, 0)
	invites := 0
	for i := 0; i < 5; i++ {
		ok, inv := g.allow("203.0.113.7", 5, true, now)
		if !ok {
			t.Fatalf("account %d refused", i)
		}
		if inv {
			invites++
		}
	}
	if invites != signupInvitesPerIPDay {
		t.Fatalf("invites credited = %d", invites)
	}
	if ok, _ := g.allow("203.0.113.7", 5, false, now); ok {
		t.Fatal("6th account allowed")
	}
	// Other address: own budget. Same IPv6 /64: shared budget.
	if ok, _ := g.allow("203.0.113.8", 5, false, now); !ok {
		t.Fatal("other ip refused")
	}
	for i := 0; i < 5; i++ {
		g.allow("2001:db8:1:2::"+string(rune('a'+i)), 5, false, now)
	}
	if ok, _ := g.allow("2001:db8:1:2:ffff::1", 5, false, now); ok {
		t.Fatal("same /64 not shared")
	}
	// Next day: fresh.
	if ok, _ := g.allow("203.0.113.7", 5, false, now.Add(24*time.Hour)); !ok {
		t.Fatal("next day refused")
	}
	// Loopback (local tools) is never limited.
	for i := 0; i < 10; i++ {
		if ok, _ := g.allow("127.0.0.1", 1, false, now); !ok {
			t.Fatal("loopback limited")
		}
	}
}
