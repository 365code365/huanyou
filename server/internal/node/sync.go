package node

import (
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
	Email string `json:"email"`
	UUID  string `json:"uuid"`
}

// Syncer periodically pulls the active user list and applies the diff to Xray.
type Syncer struct {
	APIURL   string
	Token    string
	Interval time.Duration
	Xray     *Xray

	client  http.Client
	current map[string]string // email -> uuid
}

func (s *Syncer) Run(ctx context.Context) {
	s.client.Timeout = 15 * time.Second
	s.current = map[string]string{}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.syncOnce(ctx); err != nil {
			// Keep serving the last known user set; the API being down must not cut users off.
			slog.Warn("user sync failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Syncer) syncOnce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.APIURL, "/")+"/internal/v1/users", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("api status %d: %s", resp.StatusCode, b)
	}
	var body struct {
		Users []remoteUser `json:"users"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&body); err != nil {
		return fmt.Errorf("decode users: %w", err)
	}
	want := make(map[string]string, len(body.Users))
	for _, u := range body.Users {
		want[u.Email] = u.UUID
	}

	added, removed := 0, 0
	for email, uuid := range s.current {
		if w, ok := want[email]; !ok || w != uuid {
			if err := s.Xray.RemoveUser(ctx, email); err != nil {
				slog.Warn("remove user", "err", err)
			}
			delete(s.current, email)
			removed++
		}
	}
	for email, uuid := range want {
		if _, ok := s.current[email]; ok {
			continue
		}
		if err := s.Xray.AddUser(ctx, email, uuid); err != nil {
			slog.Warn("add user", "err", err)
			continue
		}
		s.current[email] = uuid
		added++
	}
	if added+removed > 0 {
		slog.Info("users synced", "added", added, "removed", removed, "total", len(s.current))
	}
	return nil
}
