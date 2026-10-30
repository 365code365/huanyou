package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// App analytics: one row per (day, event, visitor) with a count, so UV
// (distinct visitors) and PV (counts) both come from simple GROUP BYs.
// Visitors are "u<user id>" (signed in) or "d<hash>" (before sign-in).

type EventHit struct {
	Day, Event, VKey, Platform, Version string
	Count                               int64
}

func (s *Store) RecordEvents(ctx context.Context, hits []EventHit) error {
	if len(hits) == 0 {
		return nil
	}
	cols := []string{"day", "event", "vkey", "platform", "version", "cnt", "updated_at"}
	single := s.d.upsert("app_events", cols, []string{"day", "event", "vkey"},
		"cnt = app_events.cnt + NEW(cnt)", "platform = NEW(platform)", "version = NEW(version)", "updated_at = NEW(updated_at)")
	type k struct{ d, e, v string }
	merged := map[k]*EventHit{}
	var order []k
	for _, h := range hits {
		key := k{h.Day, h.Event, h.VKey}
		if m, ok := merged[key]; ok {
			m.Count += h.Count
			continue
		}
		c := h
		merged[key] = &c
		order = append(order, key)
	}
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for len(order) > 0 {
		chunk := order[:min(len(order), syncBatch)]
		order = order[len(chunk):]
		rows := make([][]any, len(chunk))
		for i, key := range chunk {
			h := merged[key]
			rows[i] = []any{h.Day, h.Event, h.VKey, h.Platform, h.Version, h.Count, now}
		}
		q, args := multiRow(single, len(cols), rows)
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type DayStat struct {
	Day       string `json:"day"`
	UV        int64  `json:"uv"`        // distinct visitors who opened the app
	Opens     int64  `json:"opens"`     // app opens
	NewUsers  int64  `json:"new_users"` // accounts created that day
	Connects  int64  `json:"connects"`  // successful VPN connections
	Purchases int64  `json:"purchases"` // paid orders
}

// DailyStats returns n days ending today (oldest first, zero-filled).
func (s *Store) DailyStats(ctx context.Context, now time.Time, n int) ([]DayStat, error) {
	start := now.AddDate(0, 0, -(n - 1)).Format("2006-01-02")
	out := make([]DayStat, n)
	idx := map[string]int{}
	for i := range out {
		d := now.AddDate(0, 0, -(n - 1 - i)).Format("2006-01-02")
		out[i].Day, idx[d] = d, i
	}
	rows, err := s.db.QueryContext(ctx, `SELECT day, event, COUNT(*), SUM(cnt) FROM app_events
		WHERE day >= ? AND event IN ('app_open', 'vpn_connected') GROUP BY day, event`, start)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var d, e string
		var users, cnt int64
		if err := rows.Scan(&d, &e, &users, &cnt); err != nil {
			rows.Close()
			return nil, err
		}
		if i, ok := idx[d]; ok {
			if e == "app_open" {
				out[i].UV, out[i].Opens = users, cnt
			} else {
				out[i].Connects = cnt
			}
		}
	}
	rows.Close()
	trend, err := s.Trend(ctx, now, n)
	if err != nil {
		return nil, err
	}
	for i, t := range trend {
		out[i].NewUsers = t.NewUsers
	}
	p, err := s.db.QueryContext(ctx, `SELECT day, SUM(cnt) FROM app_events WHERE day >= ? AND event = 'purchase_paid' GROUP BY day`, start)
	if err != nil {
		return nil, err
	}
	defer p.Close()
	for p.Next() {
		var d string
		var c int64
		if err := p.Scan(&d, &c); err != nil {
			return nil, err
		}
		if i, ok := idx[d]; ok {
			out[i].Purchases = c
		}
	}
	return out, p.Err()
}

type EventStat struct {
	Event string `json:"event"`
	Count int64  `json:"count"`
	Users int64  `json:"users"`
}

// EventStats sums events since the day (inclusive), most frequent first.
func (s *Store) EventStats(ctx context.Context, since string) ([]EventStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event, SUM(cnt), COUNT(DISTINCT vkey) FROM app_events
		WHERE day >= ? GROUP BY event ORDER BY SUM(cnt) DESC LIMIT 200`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventStat{}
	for rows.Next() {
		var e EventStat
		if err := rows.Scan(&e.Event, &e.Count, &e.Users); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type Breakdown struct {
	Key   string `json:"key"`
	Users int64  `json:"users"`
}

// OpenBreakdown counts visitors since the day by platform or version.
func (s *Store) OpenBreakdown(ctx context.Context, since, by string) ([]Breakdown, error) {
	col := "platform"
	if by == "version" {
		col = "version"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+col+`, COUNT(DISTINCT vkey) FROM app_events
		WHERE day >= ? AND event = 'app_open' GROUP BY `+col+` ORDER BY COUNT(DISTINCT vkey) DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Breakdown{}
	for rows.Next() {
		var b Breakdown
		if err := rows.Scan(&b.Key, &b.Users); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// UniqueVisitors counts distinct visitors who opened the app since the day.
func (s *Store) UniqueVisitors(ctx context.Context, since string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT vkey) FROM app_events WHERE day >= ? AND event = 'app_open'`, since).Scan(&n)
	return n, err
}

func (s *Store) PurgeEvents(ctx context.Context, beforeDay string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM app_events WHERE day < ?`, beforeDay)
	return err
}

// ---- crash reports ----

type Crash struct {
	ID         string    `json:"id"`
	UserID     int64     `json:"user_id"`
	Platform   string    `json:"platform"`
	AppVersion string    `json:"app_version"`
	Device     string    `json:"device"`
	OSVersion  string    `json:"os_version"`
	Kind       string    `json:"kind"`
	Message    string    `json:"message"`
	Stack      string    `json:"stack,omitempty"`
	Count      int64     `json:"count"`
	Users      int64     `json:"users"`
	FirstAt    time.Time `json:"first_at"`
	LastAt     time.Time `json:"last_at"`
	Resolved   bool      `json:"resolved"`
}

// CrashID groups reports of the same crash: version + kind + message + the
// first stack lines (addresses removed).
func CrashID(version, kind, message, stack string) string {
	lines := strings.Split(stack, "\n")
	if len(lines) > 6 {
		lines = lines[:6]
	}
	h := sha256.Sum256([]byte(version + "\x00" + kind + "\x00" + message + "\x00" + strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:16])
}

// RecordCrash adds one report. newUser tells whether this user hadn't
// reported this crash before (counted in users).
func (s *Store) RecordCrash(ctx context.Context, c Crash, newUser bool) error {
	now := time.Now().Unix()
	u := int64(0)
	if newUser {
		u = 1
	}
	q := s.d.upsert("crash_reports",
		[]string{"id", "user_id", "platform", "app_version", "device", "os_version", "kind", "message", "stack", "cnt", "users", "first_at", "last_at", "resolved", "updated_at"},
		[]string{"id"},
		"cnt = crash_reports.cnt + 1", "users = crash_reports.users + NEW(users)", "user_id = NEW(user_id)",
		"device = NEW(device)", "os_version = NEW(os_version)", "last_at = NEW(last_at)", "resolved = 0", "updated_at = NEW(updated_at)")
	_, err := s.db.ExecContext(ctx, q, c.ID, c.UserID, c.Platform, c.AppVersion, c.Device, c.OSVersion, c.Kind,
		c.Message, c.Stack, 1, u, now, now, 0, now)
	return err
}

func (s *Store) Crashes(ctx context.Context, includeResolved bool, limit int) ([]Crash, error) {
	q := `SELECT id, user_id, platform, app_version, device, os_version, kind, message, cnt, users, first_at, last_at, resolved FROM crash_reports`
	if !includeResolved {
		q += ` WHERE resolved = 0`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY last_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Crash{}
	for rows.Next() {
		var c Crash
		var first, last int64
		var res int
		if err := rows.Scan(&c.ID, &c.UserID, &c.Platform, &c.AppVersion, &c.Device, &c.OSVersion, &c.Kind, &c.Message,
			&c.Count, &c.Users, &first, &last, &res); err != nil {
			return nil, err
		}
		c.FirstAt, c.LastAt, c.Resolved = time.Unix(first, 0), time.Unix(last, 0), res != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) CrashByID(ctx context.Context, id string) (*Crash, error) {
	var c Crash
	var first, last int64
	var res int
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, platform, app_version, device, os_version, kind, message, stack, cnt, users, first_at, last_at, resolved
		FROM crash_reports WHERE id = ?`, id).Scan(&c.ID, &c.UserID, &c.Platform, &c.AppVersion, &c.Device, &c.OSVersion, &c.Kind,
		&c.Message, &c.Stack, &c.Count, &c.Users, &first, &last, &res)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return nil, ErrNotFound
		}
		return nil, err
	}
	c.FirstAt, c.LastAt, c.Resolved = time.Unix(first, 0), time.Unix(last, 0), res != 0
	return &c, nil
}

func (s *Store) SetCrashResolved(ctx context.Context, id string, resolved bool) error {
	r, err := s.db.ExecContext(ctx, `UPDATE crash_reports SET resolved = ?, updated_at = ? WHERE id = ?`, b2i(resolved), time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CrashCount counts unresolved crash groups and reports since the time.
func (s *Store) CrashCount(ctx context.Context, since int64) (groups, reports int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(cnt), 0) FROM crash_reports WHERE resolved = 0 AND last_at >= ?`, since).Scan(&groups, &reports)
	return
}

func (s *Store) PurgeCrashes(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM crash_reports WHERE last_at < ?`, before)
	return err
}
