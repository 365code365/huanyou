package node

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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

	omu    sync.Mutex
	online map[string]*onlineMap // "user>>>{email}>>>online"
}

type userMeter struct {
	up, down atomic.Int64 // bytes since last Drain
	window   atomic.Int64 // bytes since the last fair-share tick
	limiter  atomic.Pointer[rate.Limiter]
	// revoked: the user is no longer allowed on this node (expired, over
	// quota, banned). Xray's RemoveUser only refuses new connections; with
	// this set, connections already open stop passing traffic (see
	// counter.Add) until Xray drops them as idle.
	revoked atomic.Bool

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

// Revoke stops (true) or resumes (false) the traffic of a user's open
// connections; used when a user is removed from / re-added to the node.
func (s *Stats) Revoke(email string, revoked bool) {
	s.meter(email).revoked.Store(revoked)
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

// Online maps: with policy statsUserOnline, Xray's dispatcher calls AddIP /
// RemoveIP on "user>>>{email}>>>online" for every connection's source
// address. They are kept here so the agent can report which addresses each
// account connects from (risk control). Only source IPs are kept, never
// destinations.

func (s *Stats) RegisterOnlineMap(name string) (stats.OnlineMap, error) {
	s.omu.Lock()
	defer s.omu.Unlock()
	if s.online == nil {
		s.online = map[string]*onlineMap{}
	}
	if m, ok := s.online[name]; ok {
		return m, nil
	}
	m := &onlineMap{live: map[string]int{}, seen: map[string]int64{}}
	s.online[name] = m
	return m, nil
}

func (s *Stats) UnregisterOnlineMap(name string) error {
	s.omu.Lock()
	delete(s.online, name)
	s.omu.Unlock()
	return nil
}

func (s *Stats) GetOnlineMap(name string) stats.OnlineMap {
	s.omu.Lock()
	defer s.omu.Unlock()
	if m, ok := s.online[name]; ok {
		return m
	}
	return nil // untyped nil: GetOrRegisterOnlineMap then registers one
}

func (s *Stats) GetAllOnlineUsers() []string {
	s.omu.Lock()
	defer s.omu.Unlock()
	var out []string
	for name, m := range s.online {
		if m.Count() > 0 {
			out = append(out, name)
		}
	}
	return out
}

// DrainIPs returns, per user email, the addresses seen since the last call:
// new connections (count) plus addresses that still have an open connection
// (count 0) so long-lived sessions keep their last-seen time fresh.
func (s *Stats) DrainIPs() map[string]map[string]int64 {
	s.omu.Lock()
	maps := make(map[string]*onlineMap, len(s.online))
	for name, m := range s.online {
		maps[name] = m
	}
	s.omu.Unlock()
	out := map[string]map[string]int64{}
	for name, m := range maps {
		parts := strings.Split(name, ">>>")
		if len(parts) != 3 || parts[0] != "user" || parts[2] != "online" {
			continue
		}
		if ips := m.drain(); len(ips) > 0 {
			out[parts[1]] = ips
		}
	}
	return out
}

func (s *Stats) RegisterChannel(string) (stats.Channel, error) { return nil, common.ErrNoClue }
func (s *Stats) UnregisterChannel(string) error                { return nil }
func (s *Stats) GetChannel(string) stats.Channel               { return nil }

// onlineMap implements stats.OnlineMap with per-IP connection counts.
type onlineMap struct {
	mu   sync.Mutex
	live map[string]int   // ip -> open connections
	seen map[string]int64 // ip -> connections opened since the last drain
	at   map[string]time.Time
}

func usableIP(ip string) bool {
	a, err := netip.ParseAddr(ip)
	// Loopback / unspecified means the real source is unknown (an internal hop).
	return err == nil && !a.IsLoopback() && !a.IsUnspecified()
}

func (m *onlineMap) AddIP(ip string) {
	if !usableIP(ip) {
		return
	}
	m.mu.Lock()
	m.live[ip]++
	m.seen[ip]++
	if m.at == nil {
		m.at = map[string]time.Time{}
	}
	m.at[ip] = time.Now()
	m.mu.Unlock()
}

func (m *onlineMap) RemoveIP(ip string) {
	m.mu.Lock()
	if n := m.live[ip]; n <= 1 {
		delete(m.live, ip)
		delete(m.at, ip)
	} else {
		m.live[ip] = n - 1
	}
	m.mu.Unlock()
}

func (m *onlineMap) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.live)
}

func (m *onlineMap) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.live))
	for ip := range m.live {
		out = append(out, ip)
	}
	return out
}

func (m *onlineMap) IPTimeMap() map[string]time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]time.Time, len(m.at))
	for ip, t := range m.at {
		out[ip] = t
	}
	return out
}

func (m *onlineMap) drain() map[string]int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.seen) == 0 && len(m.live) == 0 {
		return nil
	}
	out := make(map[string]int64, len(m.seen)+len(m.live))
	for ip := range m.live {
		out[ip] = 0
	}
	for ip, n := range m.seen {
		out[ip] = n
	}
	m.seen = map[string]int64{}
	return out
}

type counter struct {
	v      atomic.Int64
	meter  *userMeter // nil for non-user counters
	uplink bool
}

func (c *counter) Value() int64      { return c.v.Load() }
func (c *counter) Set(n int64) int64 { return c.v.Swap(n) }

// revokedHold is how long a revoked user's connection is held still: past
// Xray's idle timeout (connIdle 180 s), which then closes it.
var revokedHold = 200 * time.Second

func (c *counter) Add(n int64) int64 {
	if m := c.meter; m != nil && m.revoked.Load() {
		// Blocking here stalls this connection (as the speed limit does);
		// it resumes at once if the user is allowed again meanwhile.
		for waited := time.Duration(0); waited < revokedHold && m.revoked.Load(); waited += 500 * time.Millisecond {
			time.Sleep(500 * time.Millisecond)
		}
		if m.revoked.Load() {
			return c.v.Load()
		}
	}
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
