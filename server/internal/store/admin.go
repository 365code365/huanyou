package store

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// AdminStats is the dashboard summary.
type AdminStats struct {
	Users        int64 `json:"users"`
	NewToday     int64 `json:"new_today"`
	PaidActive   int64 `json:"paid_active"`
	Disabled     int64 `json:"disabled"`
	Orders       int64 `json:"orders"`
	RevenueCents int64 `json:"revenue_cents"`
	RevenueToday int64 `json:"revenue_today_cents"`
	TrafficToday int64 `json:"traffic_today_bytes"`
	ActiveToday  int64 `json:"active_today"` // users with traffic today
}

func (s *Store) AdminStats(ctx context.Context, now time.Time) (*AdminStats, error) {
	var a AdminStats
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	day := now.Format("2006-01-02")
	row := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM users),
			(SELECT COUNT(*) FROM users WHERE created_at >= ?),
			(SELECT COUNT(*) FROM users WHERE plan_id <> '' AND expires_at IS NOT NULL AND expires_at > ?),
			(SELECT COUNT(*) FROM users WHERE disabled = 1),
			(SELECT COUNT(*) FROM orders WHERE status = 'paid'),
			(SELECT COALESCE(SUM(amount_cents),0) FROM orders WHERE status = 'paid'),
			(SELECT COALESCE(SUM(amount_cents),0) FROM orders WHERE status = 'paid' AND paid_at >= ?),
			(SELECT COALESCE(SUM(up+down),0) FROM traffic_daily WHERE day = ?),
			(SELECT COUNT(*) FROM traffic_daily WHERE day = ?)`,
		midnight, now.Unix(), midnight, day, day)
	if err := row.Scan(&a.Users, &a.NewToday, &a.PaidActive, &a.Disabled, &a.Orders,
		&a.RevenueCents, &a.RevenueToday, &a.TrafficToday, &a.ActiveToday); err != nil {
		return nil, err
	}
	return &a, nil
}

// DailyPoint is one day of the dashboard trend chart.
type DailyPoint struct {
	Day          string `json:"day"`
	NewUsers     int64  `json:"new_users"`
	RevenueCents int64  `json:"revenue_cents"`
	TrafficBytes int64  `json:"traffic_bytes"`
}

// Trend returns the last n days (oldest first), with zero-filled gaps.
func (s *Store) Trend(ctx context.Context, now time.Time, n int) ([]DailyPoint, error) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(n - 1))
	pts := make([]DailyPoint, n)
	idx := map[string]int{}
	for i := range pts {
		d := start.AddDate(0, 0, i).Format("2006-01-02")
		pts[i].Day = d
		idx[d] = i
	}
	q := func(sqlText string, args []any, apply func(p *DailyPoint, v int64)) error {
		rows, err := s.db.QueryContext(ctx, sqlText, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			var v int64
			if err := rows.Scan(&d, &v); err != nil {
				return err
			}
			if i, ok := idx[d]; ok {
				apply(&pts[i], v)
			}
		}
		return rows.Err()
	}
	// SQLite 'localtime' matches the server timezone used for traffic_daily days.
	if err := q(`SELECT date(created_at, 'unixepoch', 'localtime') d, COUNT(*) FROM users
		WHERE created_at >= ? GROUP BY d`, []any{start.Unix()},
		func(p *DailyPoint, v int64) { p.NewUsers = v }); err != nil {
		return nil, err
	}
	if err := q(`SELECT date(paid_at, 'unixepoch', 'localtime') d, SUM(amount_cents) FROM orders
		WHERE status = 'paid' AND paid_at >= ? GROUP BY d`, []any{start.Unix()},
		func(p *DailyPoint, v int64) { p.RevenueCents = v }); err != nil {
		return nil, err
	}
	if err := q(`SELECT day, SUM(up+down) FROM traffic_daily WHERE day >= ? GROUP BY day`,
		[]any{start.Format("2006-01-02")},
		func(p *DailyPoint, v int64) { p.TrafficBytes = v }); err != nil {
		return nil, err
	}
	return pts, nil
}

// UserFilter selects users in the admin list.
type UserFilter struct {
	Query  string // email substring or invite code
	Status string // "" | "paid" | "free" | "disabled"
	Limit  int
	Offset int
}

func (f UserFilter) where(now time.Time) (string, []any) {
	var conds []string
	var args []any
	if q := strings.TrimSpace(f.Query); q != "" {
		// Match email, invite code, device id, or a numeric / "VP-xxxxx" recharge code.
		conds = append(conds, `(email LIKE ? OR ref_code = ? OR device_id = ? OR id = ?)`)
		args = append(args, "%"+q+"%", strings.ToUpper(q), q, rechargeID(q))
	}
	switch f.Status {
	case "paid":
		conds = append(conds, `plan_id <> '' AND expires_at > ? AND disabled = 0`)
		args = append(args, now.Unix())
	case "free":
		conds = append(conds, `(plan_id = '' OR expires_at IS NULL OR expires_at <= ?) AND disabled = 0`)
		args = append(args, now.Unix())
	case "disabled":
		conds = append(conds, `disabled = 1`)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// ListUsers returns a page of users matching f, newest first, and the total count.
func (s *Store) ListUsers(ctx context.Context, f UserFilter) ([]*User, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 20
	}
	where, args := f.where(time.Now())
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users`+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

// OrderFilter selects orders in the admin list.
type OrderFilter struct {
	Query  string // order id or user email substring
	Status string // "" | "paid" | "pending"
	UserID int64  // 0 = any user
	Limit  int
	Offset int
}

// AdminOrder is an order joined with its user's email.
type AdminOrder struct {
	Order
	Email      string `json:"email"`
	UserIDJSON int64  `json:"user_id"` // Order.UserID is hidden from app clients; the admin needs it
}

func (s *Store) ListOrders(ctx context.Context, f OrderFilter) ([]*AdminOrder, int64, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 20
	}
	var conds []string
	var args []any
	if q := strings.TrimSpace(f.Query); q != "" {
		conds = append(conds, `(o.id LIKE ? OR u.email LIKE ? OR o.user_id = ?)`)
		args = append(args, "%"+q+"%", "%"+q+"%", rechargeID(q))
	}
	if f.Status == "paid" || f.Status == "pending" {
		conds = append(conds, `o.status = ?`)
		args = append(args, f.Status)
	}
	if f.UserID > 0 {
		conds = append(conds, `o.user_id = ?`)
		args = append(args, f.UserID)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	from := ` FROM orders o JOIN users u ON u.id = o.user_id`
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	cols := `o.id, o.user_id, o.plan_id, o.amount_cents, o.method, o.status, o.trade_no, o.created_at, o.paid_at, COALESCE(u.email, '')`
	rows, err := s.db.QueryContext(ctx, `SELECT `+cols+from+where+` ORDER BY o.created_at DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*AdminOrder{}
	for rows.Next() {
		var ao AdminOrder
		var created int64
		var paid *int64
		if err := rows.Scan(&ao.ID, &ao.UserID, &ao.PlanID, &ao.AmountCents, &ao.Method, &ao.Status,
			&ao.TradeNo, &created, &paid, &ao.Email); err != nil {
			return nil, 0, err
		}
		ao.UserIDJSON = ao.UserID
		ao.CreatedAt = time.Unix(created, 0)
		if paid != nil {
			t := time.Unix(*paid, 0)
			ao.PaidAt = &t
		}
		out = append(out, &ao)
	}
	return out, total, rows.Err()
}

// ---- id-based user mutations for the admin panel ----

func (s *Store) updateByID(ctx context.Context, id int64, set string, args ...any) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET `+set+` WHERE id = ?`, append(args, id)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetDisabledByID(ctx context.Context, id int64, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	return s.updateByID(ctx, id, `disabled = ?`, v)
}

func (s *Store) ResetTrafficByID(ctx context.Context, id int64) error {
	return s.updateByID(ctx, id, `traffic_used = 0, period_start = ?`, time.Now().Unix())
}

func (s *Store) RotateUUIDByID(ctx context.Context, id int64) error {
	u, err := NewUUID()
	if err != nil {
		return err
	}
	return s.updateByID(ctx, id, `uuid = ?`, u)
}

func (s *Store) SetPasswordHashByID(ctx context.Context, id int64, hash string) error {
	return s.updateByID(ctx, id, `password_hash = ?`, hash)
}

func (s *Store) SetNoteByID(ctx context.Context, id int64, note string) error {
	return s.updateByID(ctx, id, `note = ?`, note)
}

// SetPlanByID sets plan and expiry directly (nil expiry clears the paid plan).
func (s *Store) SetPlanByID(ctx context.Context, id int64, planID string, expires *time.Time) error {
	if expires == nil || planID == "" {
		return s.updateByID(ctx, id, `plan_id = '', expires_at = NULL`)
	}
	return s.updateByID(ctx, id, `plan_id = ?, expires_at = ?`, planID, expires.Unix())
}

// GrantPlanByID grants/extends a plan for a user id.
func (s *Store) GrantPlanByID(ctx context.Context, id int64, planID string, days int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := grantPlanTx(ctx, tx, id, planID, days, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// CountActiveByPlan counts users whose paid plan planID is still valid.
func (s *Store) CountActiveByPlan(ctx context.Context, planID string) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE plan_id = ? AND expires_at > ?`, planID, time.Now().Unix()).Scan(&n)
	return n, err
}

// DeleteUser removes a user and their orders / traffic history.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM traffic_daily WHERE user_id = ?`,
		`DELETE FROM orders WHERE user_id = ?`,
		`UPDATE users SET invited_by = 0 WHERE invited_by = ?`,
		`UPDATE users SET primary_id = 0 WHERE primary_id = ?`, // members fall back to their own plan
		`DELETE FROM link_codes WHERE user_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// MarkOrderPaidManually completes a pending order from the admin panel (e.g. offline payment).
func (s *Store) MarkOrderPaidManually(ctx context.Context, orderID string, durationDays int) (bool, error) {
	return s.MarkPaid(ctx, orderID, "MANUAL", durationDays, time.Now())
}

// rechargeID parses "VP-00010", "vp-10" or "10" into a user id; -1 if q isn't one.
func rechargeID(q string) int64 {
	s := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(q)), "VP-")
	if s == "" {
		return -1
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}
