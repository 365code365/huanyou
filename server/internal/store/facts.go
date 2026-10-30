package store

import (
	"context"
	"database/sql"
	"time"
)

// UserFact is the per-user data segmentation works from: spend, recency,
// activity and referrals, gathered for all users in one query.
type UserFact struct {
	ID            int64
	CreatedAt     int64
	PlanID        string
	ExpiresAt     int64 // 0 = no paid plan
	Disabled      bool
	PrimaryID     int64
	InvitedBy     int64
	Platform      string
	LTVCents      int64  // total paid
	PaidOrders    int    // number of paid orders
	MaxOrderCents int64  // most expensive single order
	FirstPaidAt   int64  // 0 = never paid
	LastPaidAt    int64  // 0 = never paid
	Bytes30       int64  // traffic in the last 30 days
	ActiveDays14  int    // days with traffic in the last 14 days
	ActiveDays30  int    // days with traffic in the last 30 days
	LastActiveDay string // last day with traffic within 90 days, "" if none
	Invites       int    // users this user invited
}

// UserFacts returns one fact row per user. Aggregates are computed by the
// database in grouped subqueries (indexed by user / day), not per user.
func (s *Store) UserFacts(ctx context.Context, now time.Time) ([]UserFact, error) {
	day := func(n int) string { return now.AddDate(0, 0, -n).Format("2006-01-02") }
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.created_at, u.plan_id, COALESCE(u.expires_at, 0), u.disabled, u.primary_id, u.invited_by, u.platform,
			COALESCE(o.ltv, 0), COALESCE(o.cnt, 0), COALESCE(o.max_amt, 0), COALESCE(o.first_paid, 0), COALESCE(o.last_paid, 0),
			COALESCE(t.bytes30, 0), COALESCE(t.days14, 0), COALESCE(t.days30, 0), COALESCE(t.last_day, ''),
			COALESCE(r.invites, 0)
		FROM users u
		LEFT JOIN (
			SELECT user_id, SUM(amount_cents) AS ltv, COUNT(*) AS cnt, MAX(amount_cents) AS max_amt,
				MIN(paid_at) AS first_paid, MAX(paid_at) AS last_paid
			FROM orders WHERE status = 'paid' GROUP BY user_id
		) o ON o.user_id = u.id
		LEFT JOIN (
			SELECT user_id,
				SUM(CASE WHEN day >= ? THEN up + down ELSE 0 END) AS bytes30,
				SUM(CASE WHEN day >= ? THEN 1 ELSE 0 END) AS days14,
				SUM(CASE WHEN day >= ? THEN 1 ELSE 0 END) AS days30,
				MAX(day) AS last_day
			FROM traffic_daily WHERE day >= ? GROUP BY user_id
		) t ON t.user_id = u.id
		LEFT JOIN (
			SELECT invited_by, COUNT(*) AS invites FROM users WHERE invited_by <> 0 GROUP BY invited_by
		) r ON r.invited_by = u.id`,
		day(29), day(13), day(29), day(89))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserFact{}
	for rows.Next() {
		var f UserFact
		var disabled int
		var lastDay sql.NullString
		if err := rows.Scan(&f.ID, &f.CreatedAt, &f.PlanID, &f.ExpiresAt, &disabled, &f.PrimaryID, &f.InvitedBy, &f.Platform,
			&f.LTVCents, &f.PaidOrders, &f.MaxOrderCents, &f.FirstPaidAt, &f.LastPaidAt,
			&f.Bytes30, &f.ActiveDays14, &f.ActiveDays30, &lastDay, &f.Invites); err != nil {
			return nil, err
		}
		f.Disabled = disabled != 0
		f.LastActiveDay = lastDay.String
		out = append(out, f)
	}
	return out, rows.Err()
}

// RevenueSince sums paid orders since the given time.
func (s *Store) RevenueSince(ctx context.Context, since int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_cents), 0) FROM orders WHERE status = 'paid' AND paid_at >= ?`, since).Scan(&n)
	return n, err
}

// PlanSales is paid orders per plan.
type PlanSales struct {
	PlanID       string
	Orders       int64
	RevenueCents int64
	Buyers       int64
}

func (s *Store) PlanMix(ctx context.Context) ([]PlanSales, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plan_id, COUNT(*), COALESCE(SUM(amount_cents), 0), COUNT(DISTINCT user_id)
		FROM orders WHERE status = 'paid' GROUP BY plan_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlanSales
	for rows.Next() {
		var p PlanSales
		if err := rows.Scan(&p.PlanID, &p.Orders, &p.RevenueCents, &p.Buyers); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UsersByIDs loads users by id (any order; missing ids are skipped).
func (s *Store) UsersByIDs(ctx context.Context, ids []int64) (map[int64]*User, error) {
	out := map[int64]*User{}
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), 500)]
		ids = ids[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		list, err := scanUsers(s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE id IN (`+
			placeholders(len(chunk))+`)`, args...))
		if err != nil {
			return nil, err
		}
		for _, u := range list {
			out[u.ID] = u
		}
	}
	return out, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, n*3)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',', ' ')
		}
		b = append(b, '?')
	}
	return string(b)
}
