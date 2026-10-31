package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ---- IP records ----
//
// One row per (user, ip, source, node): source "api" is the address the app
// called the API from, "node" the address a VPN connection to that node came
// from. Rows are aggregated (hits, first / last seen), never per request, and
// old rows are purged after the retention period. Destinations are never stored.

type IPHit struct {
	UserID int64
	IP     string
	Source string // api | node
	NodeID string // node id for source "node"
	Hits   int64
	At     int64 // unix seconds of the latest hit
}

// RecordIPs merges a batch of aggregated hits.
func (s *Store) RecordIPs(ctx context.Context, hits []IPHit) error {
	if len(hits) == 0 {
		return nil
	}
	cols := []string{"user_id", "ip", "source", "node_id", "hits", "first_seen", "last_seen", "updated_at"}
	single := s.d.upsert("user_ips", cols, []string{"user_id", "ip", "source", "node_id"},
		"hits = user_ips.hits + NEW(hits)",
		"last_seen = "+s.d.greatest("user_ips.last_seen", "NEW(last_seen)"),
		"updated_at = NEW(updated_at)")
	now := time.Now().Unix()
	// Merge duplicates within the batch first (one statement can't touch a row twice on PostgreSQL).
	merged := map[ipKey]*IPHit{}
	var order []ipKey
	for _, h := range hits {
		k := ipKey{h.UserID, h.IP, h.Source, h.NodeID}
		if m, ok := merged[k]; ok {
			m.Hits += h.Hits
			m.At = max(m.At, h.At)
			continue
		}
		c := h
		merged[k] = &c
		order = append(order, k)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for len(order) > 0 {
		chunk := order[:min(len(order), syncBatch)]
		order = order[len(chunk):]
		rows := make([][]any, len(chunk))
		for i, k := range chunk {
			h := merged[k]
			rows[i] = []any{h.UserID, h.IP, h.Source, h.NodeID, h.Hits, h.At, h.At, now}
		}
		q, args := multiRow(single, len(cols), rows)
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			if isForeignKey(err) { // user deleted meanwhile: retry row by row, skipping it
				// End this transaction first: it still holds the write lock
				// (SQLite) / row locks the row-by-row retry would wait on.
				_ = tx.Rollback()
				return s.recordIPsSlow(ctx, single, merged)
			}
			return err
		}
	}
	return tx.Commit()
}

type ipKey struct {
	user               int64
	ip, source, nodeID string
}

func (s *Store) recordIPsSlow(ctx context.Context, single string, merged map[ipKey]*IPHit) error {
	now := time.Now().Unix()
	for _, h := range merged {
		_, err := s.db.ExecContext(ctx, single, h.UserID, h.IP, h.Source, h.NodeID, h.Hits, h.At, h.At, now)
		if err != nil && !isForeignKey(err) {
			return err
		}
	}
	return nil
}

func isForeignKey(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "FOREIGN KEY") || strings.Contains(m, "foreign key") || strings.Contains(m, "1452")
}

type IPRecord struct {
	UserID    int64     `json:"user_id"`
	IP        string    `json:"ip"`
	Source    string    `json:"source"`
	NodeID    string    `json:"node_id"`
	Hits      int64     `json:"hits"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

type IPFilter struct {
	UserID int64
	IP     string // exact address, or a prefix ending in "." / ":" (e.g. "1.2.3.")
	Source string
	Since  int64 // last_seen >= Since
	Limit  int
	Offset int
}

type IPPage struct {
	Items []IPRecord `json:"items"`
	Total int64      `json:"total"`
}

func (s *Store) UserIPs(ctx context.Context, f IPFilter) (*IPPage, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	var conds []string
	var args []any
	if f.UserID > 0 {
		conds = append(conds, "user_id = ?")
		args = append(args, f.UserID)
	}
	if ip := strings.TrimSpace(f.IP); ip != "" {
		if strings.HasSuffix(ip, ".") || strings.HasSuffix(ip, ":") {
			conds = append(conds, "ip LIKE ? ESCAPE '!'")
			args = append(args, likeEscape(ip)+"%")
		} else {
			conds = append(conds, "ip = ?")
			args = append(args, ip)
		}
	}
	if f.Source == "api" || f.Source == "node" {
		conds = append(conds, "source = ?")
		args = append(args, f.Source)
	}
	if f.Since > 0 {
		conds = append(conds, "last_seen >= ?")
		args = append(args, f.Since)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	page := &IPPage{Items: []IPRecord{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_ips`+where, args...).Scan(&page.Total); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, ip, source, node_id, hits, first_seen, last_seen FROM user_ips`+
		where+` ORDER BY last_seen DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r IPRecord
		var first, last int64
		if err := rows.Scan(&r.UserID, &r.IP, &r.Source, &r.NodeID, &r.Hits, &first, &last); err != nil {
			return nil, err
		}
		r.FirstSeen, r.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
		page.Items = append(page.Items, r)
	}
	return page, rows.Err()
}

// SharedIP is an address used by several subscriptions (account farming,
// shared credentials, or simply a public Wi-Fi / carrier NAT).
type SharedIP struct {
	IP       string    `json:"ip"`
	Users    int       `json:"users"` // distinct subscriptions (members count as their owner)
	Hits     int64     `json:"hits"`
	LastSeen time.Time `json:"last_seen"`
	UserIDs  []int64   `json:"user_ids"` // up to 20
}

const ownerExpr = `CASE WHEN u.primary_id <> 0 THEN u.primary_id ELSE u.id END`

func (s *Store) SharedIPs(ctx context.Context, since int64, minUsers, limit int) ([]SharedIP, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.ip, COUNT(DISTINCT `+ownerExpr+`), SUM(i.hits), MAX(i.last_seen)
		FROM user_ips i JOIN users u ON u.id = i.user_id
		WHERE i.last_seen >= ?
		GROUP BY i.ip
		HAVING COUNT(DISTINCT `+ownerExpr+`) >= ?
		ORDER BY COUNT(DISTINCT `+ownerExpr+`) DESC, MAX(i.last_seen) DESC
		LIMIT ?`, since, minUsers, limit)
	if err != nil {
		return nil, err
	}
	var out []SharedIP
	for rows.Next() {
		var r SharedIP
		var last int64
		if err := rows.Scan(&r.IP, &r.Users, &r.Hits, &last); err != nil {
			rows.Close()
			return nil, err
		}
		r.LastSeen = time.Unix(last, 0)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ids, err := s.int64s(ctx, `SELECT DISTINCT user_id FROM user_ips WHERE ip = ? AND last_seen >= ? ORDER BY user_id LIMIT 20`,
			out[i].IP, since)
		if err != nil {
			return nil, err
		}
		out[i].UserIDs = ids
	}
	return out, nil
}

// MultiIPUser is an account seen from many addresses (credential sharing / resale).
type MultiIPUser struct {
	UserID   int64     `json:"user_id"`
	IPs      int       `json:"ips"`
	Nodes    int       `json:"nodes"`
	LastSeen time.Time `json:"last_seen"`
}

func (s *Store) MultiIPUsers(ctx context.Context, since int64, minIPs, limit int) ([]MultiIPUser, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, COUNT(DISTINCT ip), COUNT(DISTINCT node_id), MAX(last_seen)
		FROM user_ips WHERE last_seen >= ?
		GROUP BY user_id HAVING COUNT(DISTINCT ip) >= ?
		ORDER BY COUNT(DISTINCT ip) DESC LIMIT ?`, since, minIPs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MultiIPUser
	for rows.Next() {
		var r MultiIPUser
		var last int64
		if err := rows.Scan(&r.UserID, &r.IPs, &r.Nodes, &last); err != nil {
			return nil, err
		}
		r.LastSeen = time.Unix(last, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// IPCounts returns distinct IPs per user seen since the given time.
func (s *Store) IPCounts(ctx context.Context, since int64) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, COUNT(DISTINCT ip) FROM user_ips WHERE last_seen >= ? GROUP BY user_id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// PurgeIPs deletes IP records last seen before the cutoff.
func (s *Store) PurgeIPs(ctx context.Context, before int64) (int64, error) {
	return s.deleteBatched(ctx, "user_ips", "last_seen < ?", before)
}

// deleteBatched deletes the rows matching where a few thousand at a time,
// so a large purge never holds the write lock (SQLite) or many row locks for
// long while traffic reports and sign-ins wait.
func (s *Store) deleteBatched(ctx context.Context, table, where string, args ...any) (int64, error) {
	const batch = 5000
	var q string
	switch {
	case s.d.mysql():
		q = fmt.Sprintf(`DELETE FROM %s WHERE %s LIMIT %d`, table, where, batch)
	case s.d.pg():
		q = fmt.Sprintf(`DELETE FROM %s WHERE ctid IN (SELECT ctid FROM %s WHERE %s LIMIT %d)`, table, table, where, batch)
	default:
		q = fmt.Sprintf(`DELETE FROM %s WHERE rowid IN (SELECT rowid FROM %s WHERE %s LIMIT %d)`, table, table, where, batch)
	}
	var total int64
	for {
		r, err := s.db.ExecContext(ctx, q, args...)
		if err != nil {
			return total, err
		}
		n, _ := r.RowsAffected()
		total += n
		if n < batch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

func (s *Store) int64s(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var v int64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- risk rules ----

// RiskRule blocks an IP / CIDR (kind "ip") or a user id (kind "user").
type RiskRule struct {
	Kind      string     `json:"kind"`
	Value     string     `json:"value"`
	Reason    string     `json:"reason"`
	ExpiresAt *time.Time `json:"expires_at"` // nil = permanent
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// Active reports whether the rule applies at now.
func (r RiskRule) Active(now time.Time) bool { return r.ExpiresAt == nil || now.Before(*r.ExpiresAt) }

func (s *Store) PutRiskRule(ctx context.Context, r RiskRule) error {
	now := time.Now().Unix()
	var exp any
	if r.ExpiresAt != nil {
		exp = r.ExpiresAt.Unix()
	}
	q := s.d.upsert("risk_rules", []string{"kind", "value", "reason", "expires_at", "created_at", "updated_at"},
		[]string{"kind", "value"}, "reason = NEW(reason)", "expires_at = NEW(expires_at)", "updated_at = NEW(updated_at)")
	_, err := s.db.ExecContext(ctx, q, r.Kind, r.Value, r.Reason, exp, now, now)
	return err
}

func (s *Store) DeleteRiskRule(ctx context.Context, kind, value string) error {
	r, err := s.db.ExecContext(ctx, `DELETE FROM risk_rules WHERE kind = ? AND value = ?`, kind, value)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RiskRules returns every rule (including expired ones), newest first.
func (s *Store) RiskRules(ctx context.Context) ([]RiskRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, value, reason, expires_at, created_at, updated_at FROM risk_rules ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RiskRule{}
	for rows.Next() {
		var r RiskRule
		var exp sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&r.Kind, &r.Value, &r.Reason, &exp, &created, &updated); err != nil {
			return nil, err
		}
		if exp.Valid {
			t := time.Unix(exp.Int64, 0)
			r.ExpiresAt = &t
		}
		r.CreatedAt, r.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PurgeExpiredRiskRules removes rules that expired before the cutoff.
func (s *Store) PurgeExpiredRiskRules(ctx context.Context, before int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM risk_rules WHERE expires_at IS NOT NULL AND expires_at < ?`, before)
	return err
}
