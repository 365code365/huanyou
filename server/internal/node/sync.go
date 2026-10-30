package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type remoteUser struct {
	Email     string `json:"email"`
	UUID      string `json:"uuid"`
	SpeedKbps int    `json:"speed_kbps"` // 0 = unlimited
}

// Syncer periodically (1) reports per-user traffic to the API and
// (2) pulls the allowed user list, applying the diff and speed limits to Xray.
type Syncer struct {
	APIURL   string
	Token    string
	Interval time.Duration
	Xray     *Xray
	Stats    *Stats
	Fair     *FairShare // optional; receives the node bandwidth from the API

	client  http.Client
	current map[string]string // email -> uuid
	etag    string            // last user list version; the API answers 304 if unchanged
	blocked string            // last applied blocked-source list (joined), to skip no-op updates
}

func (s *Syncer) Run(ctx context.Context) {
	s.client.Timeout = 15 * time.Second
	s.current = map[string]string{}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	redial := time.Now()
	for {
		// A polled keep-alive connection never goes idle long enough to be
		// closed, so it would stay on the old IP forever after the API's
		// domain moves to another server. Re-dial (and re-resolve) every 5 min.
		if time.Since(redial) > 5*time.Minute {
			s.client.CloseIdleConnections()
			redial = time.Now()
		}
		if err := s.reportTraffic(ctx); err != nil {
			slog.Warn("traffic report failed", "err", err)
		}
		if err := s.syncUsers(ctx); err != nil {
			// Keep serving the last known user set; the API being down must not cut users off.
			slog.Warn("user sync failed", "err", err)
		}
		select {
		case <-ctx.Done():
			// Final flush so traffic of the last interval is not lost.
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.reportTraffic(flushCtx)
			cancel()
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.APIURL, "/")+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, b)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

func (s *Syncer) reportTraffic(ctx context.Context) error {
	usage := s.Stats.Drain()
	ips := s.Stats.DrainIPs()
	if len(usage) == 0 && len(ips) == 0 {
		return nil
	}
	type item struct {
		Email string `json:"email"`
		Up    int64  `json:"up"`
		Down  int64  `json:"down"`
	}
	items := make([]item, 0, len(usage))
	for email, u := range usage {
		items = append(items, item{email, u.Up, u.Down})
	}
	// ips: {email: {source ip: new connections}} — source addresses only.
	if err := s.do(ctx, http.MethodPost, "/internal/v1/traffic", map[string]any{"traffic": items, "ips": ips}, nil); err != nil {
		s.Stats.Restore(usage) // retry next round
		return err
	}
	return nil
}

func (s *Syncer) syncUsers(ctx context.Context) error {
	var body struct {
		Users         []remoteUser `json:"users"`
		BandwidthMbps int          `json:"bandwidth_mbps"`
		BlockedIPs    []string     `json:"blocked_ips"` // risk control: drop traffic from these sources
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.APIURL, "/")+"/internal/v1/users", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	if s.etag != "" {
		req.Header.Set("If-None-Match", s.etag)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil // nothing changed since the last sync
	case http.StatusOK:
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("GET /internal/v1/users: status %d: %s", resp.StatusCode, b)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return err
	}
	if s.Fair != nil {
		s.Fair.SetBandwidth(body.BandwidthMbps)
	}
	if key := strings.Join(body.BlockedIPs, ","); key != s.blocked {
		if err := s.Xray.SetBlockedSources(body.BlockedIPs); err != nil {
			slog.Warn("apply blocked sources", "err", err)
		} else {
			s.blocked = key
			slog.Info("blocked sources updated", "count", len(body.BlockedIPs))
		}
	}
	want := make(map[string]remoteUser, len(body.Users))
	for _, u := range body.Users {
		want[u.Email] = u
	}

	added, removed := 0, 0
	for email, uuid := range s.current {
		if w, ok := want[email]; !ok || w.UUID != uuid {
			if err := s.Xray.RemoveUser(ctx, email); err != nil {
				slog.Warn("remove user", "err", err)
			}
			delete(s.current, email)
			removed++
		}
	}
	for email, u := range want {
		s.Stats.SetSpeed(email, u.SpeedKbps)
		if _, ok := s.current[email]; ok {
			continue
		}
		if err := s.Xray.AddUser(ctx, email, u.UUID); err != nil {
			slog.Warn("add user", "err", err)
			continue
		}
		s.current[email] = u.UUID
		added++
	}
	if added+removed > 0 {
		slog.Info("users synced", "added", added, "removed", removed, "total", len(s.current))
	}
	s.etag = resp.Header.Get("ETag")
	return nil
}
