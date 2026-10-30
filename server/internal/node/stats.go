package node

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/features/stats"
	"golang.org/x/time/rate"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Stats replaces Xray's stats manager inside the node. Xray's dispatcher
// calls Counter.Add(n) synchronously before writing each chunk of a user's
// traffic, so the counter is also where we enforce per-user speed limits:
// blocking in Add applies back-pressure to that connection.
//
// Counter names used by Xray: "user>>>{email}>>>traffic>>>uplink|downlink".
type Stats struct {
	mu       sync.Mutex
	counters map[string]*counter
	users    map[string]*userMeter
}

type userMeter struct {
	up, down atomic.Int64 // bytes since last Drain
	window   atomic.Int64 // bytes since the last fair-share tick
	limiter  atomic.Pointer[rate.Limiter]

	mu      sync.Mutex
	planBps float64 // plan speed limit, bytes/s (0 = unlimited)
	dynBps  float64 // fair-share cap set under load, bytes/s (0 = none)
}

// effective is the tighter of the plan limit and the fair-share cap (0 = none).
func (m *userMeter) effective() float64 {
	switch {
	case m.planBps == 0:
		return m.dynBps
	case m.dynBps == 0:
		return m.planBps
	default:
		return min(m.planBps, m.dynBps)
	}
}

// apply swaps in a limiter matching the effective limit. Callers hold m.mu.
func (m *userMeter) apply() {
	bps := m.effective()
	if bps <= 0 {
		m.limiter.Store(nil)
		return
	}
	if old := m.limiter.Load(); old != nil && old.Limit() == rate.Limit(bps) {
		return
	}
	burst := int(bps)
	if burst < 32<<10 {
		burst = 32 << 10
	}
	m.limiter.Store(rate.NewLimiter(rate.Limit(bps), burst))
}

func NewStats() *Stats {
	return &Stats{counters: map[string]*counter{}, users: map[string]*userMeter{}}
}

var statsInstance *Stats

// Register makes Stats creatable from an Xray config entry
// (serial.ToTypedMessage(&emptypb.Empty{})). Xray only builds features from
// registered protobuf config types, and Empty is not used by Xray itself.
func registerStats(s *Stats) {
	if statsInstance == nil {
		common.Must(common.RegisterConfig((*emptypb.Empty)(nil), func(context.Context, interface{}) (interface{}, error) {
			return statsInstance, nil
		}))
	}
	statsInstance = s
}

func (s *Stats) meter(email string) *userMeter {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.users[email]
	if m == nil {
		m = &userMeter{}
		s.users[email] = m
	}
	return m
}

// SetSpeed sets the user's plan limit in kbit/s (0 = unlimited). Applies to new and existing connections.
func (s *Stats) SetSpeed(email string, kbps int) {
	m := s.meter(email)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.planBps = 0
	if kbps > 0 {
		m.planBps = float64(kbps) * 1000 / 8
	}
	m.apply()
}

// Usage is traffic since the previous Drain.
type Usage struct{ Up, Down int64 }

// Drain returns and resets per-user traffic counters.
func (s *Stats) Drain() map[string]Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Usage{}
	for email, m := range s.users {
		u := Usage{Up: m.up.Swap(0), Down: m.down.Swap(0)}
		if u.Up != 0 || u.Down != 0 {
			out[email] = u
		}
	}
	return out
}

// Restore adds back usage that could not be reported.
func (s *Stats) Restore(pending map[string]Usage) {
	for email, u := range pending {
		m := s.meter(email)
		m.up.Add(u.Up)
		m.down.Add(u.Down)
	}
}

// --- stats.Manager implementation ---

func (s *Stats) Type() interface{} { return stats.ManagerType() }
func (s *Stats) Start() error      { return nil }
func (s *Stats) Close() error      { return nil }

func (s *Stats) RegisterCounter(name string) (stats.Counter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.counters[name]; ok {
		return c, nil
	}
	c := &counter{}
	if parts := strings.Split(name, ">>>"); len(parts) == 4 && parts[0] == "user" && parts[2] == "traffic" {
		email := parts[1]
		m := s.users[email]
		if m == nil {
			m = &userMeter{}
			s.users[email] = m
		}
		c.meter = m
		c.uplink = parts[3] == "uplink"
	}
	s.counters[name] = c
	return c, nil
}

func (s *Stats) UnregisterCounter(name string) error {
	s.mu.Lock()
	delete(s.counters, name)
	s.mu.Unlock()
	return nil
}

func (s *Stats) GetCounter(name string) stats.Counter {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.counters[name]; ok {
		return c
	}
	return nil
}

func (s *Stats) RegisterOnlineMap(string) (stats.OnlineMap, error) { return nil, common.ErrNoClue }
func (s *Stats) UnregisterOnlineMap(string) error                  { return nil }
func (s *Stats) GetOnlineMap(string) stats.OnlineMap               { return nil }
func (s *Stats) RegisterChannel(string) (stats.Channel, error)     { return nil, common.ErrNoClue }
func (s *Stats) UnregisterChannel(string) error                    { return nil }
func (s *Stats) GetChannel(string) stats.Channel                   { return nil }
func (s *Stats) GetAllOnlineUsers() []string                       { return nil }

type counter struct {
	v      atomic.Int64
	meter  *userMeter // nil for non-user counters
	uplink bool
}

func (c *counter) Value() int64      { return c.v.Load() }
func (c *counter) Set(n int64) int64 { return c.v.Swap(n) }

func (c *counter) Add(n int64) int64 {
	if m := c.meter; m != nil && n > 0 {
		if c.uplink {
			m.up.Add(n)
		} else {
			m.down.Add(n)
		}
		m.window.Add(n)
		if l := m.limiter.Load(); l != nil {
			wait(l, n)
		}
	}
	return c.v.Add(n) - n
}

// wait blocks until n bytes may pass the limiter (WaitN rejects n > burst, so chunk it).
func wait(l *rate.Limiter, n int64) {
	burst := int64(l.Burst())
	for n > 0 {
		step := min(n, burst)
		_ = l.WaitN(context.Background(), int(step))
		n -= step
	}
}
