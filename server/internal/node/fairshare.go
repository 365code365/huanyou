package node

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// FairShare keeps a node usable for everyone when it is busy.
//
// Every tick it measures each user's throughput. While total throughput stays
// below the high-water mark nobody is touched. Above it, the bandwidth is
// split max-min fairly: users below the fair share keep their speed, and the
// heavy users are capped so that together they use what's left. That way one
// large download can't starve hundreds of people browsing. When load falls
// below the low-water mark the caps are relaxed step by step until removed.
// Plan speed limits always still apply on top.
type FairShare struct {
	Stats    *Stats
	Interval time.Duration // default 2 s

	capBps atomic.Int64 // usable bandwidth in bytes/s (0 = disabled)
}

const (
	highWater  = 0.85             // start sharing above 85 % of capacity
	lowWater   = 0.60             // start relaxing below 60 %
	minShare   = 512 * 1000 / 8.0 // never cap anyone below 512 kbit/s
	relaxStep  = 1.5              // caps grow ×1.5 per tick while relaxing
	activeFrom = 4 << 10          // ≥ 4 KB in a tick counts as an active user
)

// SetBandwidth sets the node capacity in Mbit/s (0 disables fair sharing).
func (f *FairShare) SetBandwidth(mbps int) {
	old := f.capBps.Swap(int64(mbps) * 1000 * 1000 / 8)
	if old != f.capBps.Load() {
		slog.Info("fair share", "bandwidth_mbps", mbps)
	}
}

func (f *FairShare) Run(ctx context.Context) {
	iv := f.Interval
	if iv <= 0 {
		iv = 2 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.tick(iv.Seconds())
		}
	}
}

func (f *FairShare) tick(secs float64) {
	f.Stats.mu.Lock()
	meters := make([]*userMeter, 0, len(f.Stats.users))
	for _, m := range f.Stats.users {
		meters = append(meters, m)
	}
	f.Stats.mu.Unlock()

	rates := make([]float64, len(meters))
	var total float64
	active := 0
	for i, m := range meters {
		b := m.window.Swap(0)
		rates[i] = float64(b) / secs
		total += rates[i]
		if b >= activeFrom {
			active++
		}
	}
	capacity := float64(f.capBps.Load())
	if capacity <= 0 {
		f.release(meters) // disabled: make sure nothing stays capped
		return
	}

	switch {
	case total > capacity*highWater && active > 1:
		share := maxMinShare(rates, capacity)
		if share < minShare {
			share = minShare
		}
		capped := 0
		for i, m := range meters {
			if rates[i] < share*0.9 {
				continue // light user: leave alone
			}
			m.mu.Lock()
			if m.dynBps == 0 || m.dynBps > share {
				m.dynBps = share
				m.apply()
			}
			m.mu.Unlock()
			capped++
		}
		slog.Debug("fair share: capping", "total_mbps", total*8/1e6, "share_mbps", share*8/1e6, "capped", capped)
	case total < capacity*lowWater:
		for _, m := range meters {
			m.mu.Lock()
			if m.dynBps > 0 {
				m.dynBps *= relaxStep
				if m.dynBps >= capacity {
					m.dynBps = 0
				}
				m.apply()
			}
			m.mu.Unlock()
		}
	}
}

func (f *FairShare) release(meters []*userMeter) {
	for _, m := range meters {
		m.mu.Lock()
		if m.dynBps != 0 {
			m.dynBps = 0
			m.apply()
		}
		m.mu.Unlock()
	}
}

// maxMinShare returns the per-user cap that splits capacity max-min fairly
// given the users' current rates (water-filling): users using less than
// their share keep it, and the rest is divided evenly among the others.
func maxMinShare(rates []float64, capacity float64) float64 {
	left, n := capacity, 0
	for _, r := range rates {
		if r > 0 {
			n++
		}
	}
	if n == 0 {
		return capacity
	}
	// Repeatedly give everyone below the current share what they use.
	used := make([]bool, len(rates))
	for {
		share := left / float64(n)
		changed := false
		for i, r := range rates {
			if r > 0 && !used[i] && r <= share {
				used[i] = true
				left -= r
				n--
				changed = true
			}
		}
		if !changed || n == 0 {
			if n == 0 {
				return capacity
			}
			return left / float64(n)
		}
	}
}
