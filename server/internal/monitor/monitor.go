// Package monitor health-checks proxy nodes and raises alerts.
//
// Two independent signals are tracked per node:
//   - probe: a TCP connect to the node's public port from the API server
//     (is the proxy reachable at all?)
//   - heartbeat: the node agent's periodic user sync with the API
//     (is the agent alive and receiving user changes?)
//
// State transitions (down / recovered / agent lost) are recorded as events and
// pushed to the configured webhook.
package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"vpnplatform/server/internal/config"
)

const historyLen = 60 // probe results kept per node (≈1h at the default interval)

// Status is a node's current health.
type Status struct {
	Online       bool      `json:"online"`     // probe result after fail_threshold debouncing
	LatencyMs    int       `json:"latency_ms"` // last successful probe
	Uptime       float64   `json:"uptime"`     // share of successful probes in history (0..1)
	History      []Probe   `json:"history"`    // oldest first
	LastCheck    time.Time `json:"last_check"`
	LastChange   time.Time `json:"last_change"`
	AgentSeen    time.Time `json:"agent_seen"`    // last node-agent sync
	AgentOnline  bool      `json:"agent_online"`  // agent synced recently
	Users        int       `json:"users"`         // users pushed to the node in the last sync
	TrafficBytes int64     `json:"traffic_bytes"` // reported by the node since API start
	ActiveUsers  int       `json:"active_users"`  // users with traffic in the latest report
	ActiveAt     time.Time `json:"-"`
	fails        int
	agentAlerted bool
	probed       bool
}

type Probe struct {
	At        time.Time `json:"at"`
	OK        bool      `json:"ok"`
	LatencyMs int       `json:"latency_ms"`
}

// Event is an alert-worthy state change.
type Event struct {
	At     time.Time `json:"at"`
	NodeID string    `json:"node_id"`
	Level  string    `json:"level"` // "error" | "warning" | "info"
	Text   string    `json:"text"`
}

type Monitor struct {
	cat *config.LiveCatalog
	hc  http.Client

	mu     sync.RWMutex
	st     map[string]*Status
	events []Event // newest last, capped

	expiryAlerted map[string]string // node id -> day last alerted
}

func New(cat *config.LiveCatalog) *Monitor {
	return &Monitor{cat: cat, st: map[string]*Status{}, hc: http.Client{Timeout: 10 * time.Second}}
}

// Status returns a copy of a node's status. Unknown / not-yet-probed nodes report online,
// so the app doesn't show a fresh node as down before the first probe.
func (m *Monitor) Status(id string) Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.st[id]
	if !ok || !s.probed {
		out := Status{Online: true, Uptime: 1}
		if ok {
			out.AgentSeen, out.AgentOnline, out.Users, out.TrafficBytes = s.AgentSeen, s.AgentOnline, s.Users, s.TrafficBytes
			out.ActiveUsers = s.currentActive()
		}
		return out
	}
	out := *s
	out.ActiveUsers = s.currentActive()
	out.History = append([]Probe(nil), s.History...)
	return out
}

// currentActive treats a load figure older than 5 minutes as stale (no reports = idle).
func (s *Status) currentActive() int {
	if time.Since(s.ActiveAt) > 5*time.Minute {
		return 0
	}
	return s.ActiveUsers
}

// Events returns recent events, newest first.
func (m *Monitor) Events(limit int) []Event {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Event, 0, min(limit, len(m.events)))
	for i := len(m.events) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.events[i])
	}
	return out
}

func (m *Monitor) status(id string) *Status {
	s := m.st[id]
	if s == nil {
		s = &Status{Online: true, LastChange: time.Now()}
		m.st[id] = s
	}
	return s
}

// Heartbeat records a node-agent sync.
func (m *Monitor) Heartbeat(nodeID string, users int) {
	m.mu.Lock()
	s := m.status(nodeID)
	recovered := s.agentAlerted
	s.AgentSeen, s.AgentOnline, s.Users, s.agentAlerted = time.Now(), true, users, false
	m.mu.Unlock()
	if recovered {
		m.emit(nodeID, "info", fmt.Sprintf("节点代理恢复同步：%s", nodeID))
	}
}

// AddTraffic records traffic reported by a node. active is how many users
// moved data in this report; it is the node's current load.
func (m *Monitor) AddTraffic(nodeID string, bytes int64, active int) {
	m.mu.Lock()
	s := m.status(nodeID)
	s.TrafficBytes += bytes
	s.ActiveUsers, s.ActiveAt = active, time.Now()
	m.mu.Unlock()
}

// Forget drops state for a deleted node.
func (m *Monitor) Forget(nodeID string) {
	m.mu.Lock()
	delete(m.st, nodeID)
	m.mu.Unlock()
}

// CheckNow probes all nodes immediately (admin "立即检测").
func (m *Monitor) CheckNow(ctx context.Context) { m.checkAll(ctx) }

// Run probes nodes until ctx is done. Settings are re-read every round, so
// admin changes (interval, enable/disable) apply without a restart.
func (m *Monitor) Run(ctx context.Context) {
	for {
		mc := m.cat.Get().Monitor
		if mc.Enabled {
			m.checkAll(ctx)
		}
		m.checkExpiry(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(mc.IntervalSeconds) * time.Second):
		}
	}
}

func (m *Monitor) checkAll(ctx context.Context) {
	cat := m.cat.Get()
	var wg sync.WaitGroup
	for i := range cat.Nodes {
		n := cat.Nodes[i]
		if !n.Serving() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.check(ctx, &n, cat.Monitor)
		}()
	}
	wg.Wait()
}

func (m *Monitor) check(ctx context.Context, n *config.Node, mc config.MonitorConfig) {
	addr := net.JoinHostPort(n.Address, fmt.Sprint(n.VisionPort))
	start := time.Now()
	d := net.Dialer{Timeout: time.Duration(mc.TimeoutSeconds) * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	latency := int(time.Since(start).Milliseconds())
	if conn != nil {
		conn.Close()
	}
	ok := err == nil

	m.mu.Lock()
	s := m.status(n.ID)
	s.probed = true
	s.LastCheck = time.Now()
	s.History = append(s.History, Probe{At: s.LastCheck, OK: ok, LatencyMs: latency})
	if len(s.History) > historyLen {
		s.History = s.History[len(s.History)-historyLen:]
	}
	good := 0
	for _, p := range s.History {
		if p.OK {
			good++
		}
	}
	s.Uptime = float64(good) / float64(len(s.History))

	var event, level string
	if ok {
		s.LatencyMs = latency
		if !s.Online {
			s.Online, s.LastChange = true, time.Now()
			event, level = fmt.Sprintf("🟢 节点恢复：%s（%s），延迟 %dms", n.Name, n.ID, latency), "info"
		}
		s.fails = 0
	} else {
		s.fails++
		if s.Online && s.fails >= mc.FailThreshold {
			s.Online, s.LastChange = false, time.Now()
			event, level = fmt.Sprintf("🔴 节点离线：%s（%s %s），错误：%v", n.Name, n.ID, addr, err), "error"
		}
	}
	// Agent heartbeat: the agent syncs every ~30s; 3 minutes of silence means it's gone.
	var agentEvent string
	if !s.AgentSeen.IsZero() && time.Since(s.AgentSeen) > 3*time.Minute {
		s.AgentOnline = false
		if !s.agentAlerted {
			s.agentAlerted = true
			agentEvent = fmt.Sprintf("🟠 节点代理失联：%s（%s），最后同步 %s", n.Name, n.ID, s.AgentSeen.Format("15:04:05"))
		}
	}
	m.mu.Unlock()

	if event != "" {
		m.emit(n.ID, level, event)
	}
	if agentEvent != "" {
		m.emit(n.ID, "warning", agentEvent)
	}
}

// emit records an event and posts it to the alert webhook.
func (m *Monitor) emit(nodeID, level, text string) {
	m.mu.Lock()
	m.events = append(m.events, Event{At: time.Now(), NodeID: nodeID, Level: level, Text: text})
	if len(m.events) > 200 {
		m.events = m.events[len(m.events)-200:]
	}
	m.mu.Unlock()
	slog.Warn("monitor event", "node", nodeID, "level", level, "text", text)
	go m.post(text)
}

// TestAlert sends a test message to the webhook and returns the delivery error.
func (m *Monitor) TestAlert() error {
	return m.post("✅ VPN 预警测试消息")
}

func (m *Monitor) post(text string) error {
	url := m.cat.Get().Monitor.AlertWebhook
	if url == "" {
		return fmt.Errorf("未配置预警 Webhook")
	}
	var payload any
	switch m.cat.Get().Monitor.AlertFormat {
	case "feishu":
		payload = map[string]any{"msg_type": "text", "content": map[string]string{"text": text}}
	case "dingtalk", "wecom":
		payload = map[string]any{"msgtype": "text", "text": map[string]string{"content": text}}
	default: // generic / Slack-compatible
		payload = map[string]string{"text": text}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.hc.Do(req)
	if err != nil {
		slog.Warn("alert webhook failed", "err", err)
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回 %d", resp.StatusCode)
	}
	return nil
}

// checkExpiry raises one event (and webhook alert) per node per day while its
// server rental expires within 7 days, so renewals are not missed.
func (m *Monitor) checkExpiry(now time.Time) {
	day := now.Format("2006-01-02")
	for _, n := range m.cat.Get().ExpiringNodes(now, 7*24*time.Hour) {
		m.mu.Lock()
		if m.expiryAlerted == nil {
			m.expiryAlerted = map[string]string{}
		}
		done := m.expiryAlerted[n.ID] == day
		m.expiryAlerted[n.ID] = day
		m.mu.Unlock()
		if done {
			continue
		}
		left := n.ExpiresAt.Sub(now)
		vendor := ""
		if n.Vendor != "" {
			vendor = "（" + n.Vendor + "）"
		}
		var text string
		if left <= 0 {
			text = fmt.Sprintf("⛔ 节点「%s」%s 服务器已于 %s 到期，请立即续费", n.Name, vendor, n.ExpiresAt.Local().Format("2006-01-02 15:04"))
		} else {
			text = fmt.Sprintf("⏰ 节点「%s」%s 服务器将于 %s 到期（剩 %.0f 小时），请及时续费", n.Name, vendor, n.ExpiresAt.Local().Format("2006-01-02 15:04"), left.Hours())
		}
		m.emit(n.ID, "warning", text)
	}
}
