package node

import (
	"math"
	"testing"
)

func TestMaxMinShare(t *testing.T) {
	// 100 units of capacity; two light users (5, 10) keep theirs, the other
	// three split the remaining 85 equally.
	got := maxMinShare([]float64{5, 10, 80, 60, 40}, 100)
	if math.Abs(got-85.0/3) > 1e-9 {
		t.Fatalf("share = %v, want %v", got, 85.0/3)
	}
	// Everyone light: no cap needed (returns capacity).
	if got := maxMinShare([]float64{1, 2, 3}, 100); got != 100 {
		t.Fatalf("all light: %v", got)
	}
}

func TestFairShareCapsHeavyUsers(t *testing.T) {
	st := NewStats()
	heavy, light := st.meter("u1"), st.meter("u2")
	f := &FairShare{Stats: st}
	f.SetBandwidth(10) // 10 Mbit/s = 1.25 MB/s

	// One tick of 1 s: heavy moved 1.2 MB, light 50 KB -> over 85 % of capacity.
	heavy.window.Store(1_200_000)
	light.window.Store(50_000)
	f.tick(1)
	if heavy.limiter.Load() == nil {
		t.Fatal("heavy user not capped under load")
	}
	if light.limiter.Load() != nil {
		t.Fatal("light user should not be capped")
	}
	if c := heavy.dynBps; c < 1_100_000 || c > 1_250_000 {
		t.Fatalf("heavy cap %v not ≈ capacity minus light user", c)
	}

	// Load drops: caps relax and eventually disappear.
	for i := 0; i < 20 && heavy.limiter.Load() != nil; i++ {
		f.tick(1)
	}
	if heavy.limiter.Load() != nil {
		t.Fatal("cap not released after load dropped")
	}
}

func TestFairShareKeepsPlanLimit(t *testing.T) {
	st := NewStats()
	st.SetSpeed("u1", 1000) // plan: 1 Mbit/s
	m := st.meter("u1")
	m.mu.Lock()
	m.dynBps = 10_000_000 // fair-share cap looser than the plan
	m.apply()
	m.mu.Unlock()
	if got := float64(m.limiter.Load().Limit()); got != 125_000 {
		t.Fatalf("plan limit must still apply, got %v B/s", got)
	}
}
