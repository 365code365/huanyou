// Package store persists users, orders and traffic in SQLite (pure Go driver, no cgo).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrEmailTaken  = errors.New("email already registered")
	ErrDeviceTaken = errors.New("device already registered")
)

type User struct {
	ID           int64
	Email        string // "" for device-only accounts (no email/password)
	PasswordHash string
	UUID         string // VLESS credential, unique per user
	Disabled     bool   // admin ban, overrides everything
	PlanID       string // paid plan; "" = none
	ExpiresAt    *time.Time
	PeriodStart  time.Time // start of the current traffic period
	TrafficUsed  int64     // bytes (up+down) in the current period
	RefCode      string    // this user's own invite code
	InvitedBy    int64     // user id of the inviter, 0 = none
	Note         string    // admin note
	DeviceID     string    // stable per-device identity (keychain / SSAID); "" if none
	Platform     string    // "ios" | "android" | "" — informational
	PrimaryID    int64     // if non-zero, this device shares the plan of user PrimaryID
	CreatedAt    time.Time
}

// Linked reports whether this device follows another account's subscription.
func (u *User) Linked() bool { return u.PrimaryID != 0 }

// PaidPlanActive reports whether the user's paid plan is still valid.
func (u *User) PaidPlanActive(now time.Time) bool {
	return u.PlanID != "" && u.ExpiresAt != nil && now.Before(*u.ExpiresAt)
}

// UsedInPeriod returns traffic used in the period containing now (0 if the stored period already ended).
func (u *User) UsedInPeriod(now time.Time, period time.Duration) int64 {
	if !now.Before(u.PeriodStart.Add(period)) {
		return 0
	}
	return u.TrafficUsed
}

// PeriodEnd returns when the current traffic period ends.
func (u *User) PeriodEnd(now time.Time, period time.Duration) time.Time {
	start := u.PeriodStart
	for !now.Before(start.Add(period)) {
		start = start.Add(period)
	}
	return start.Add(period)
}

// XrayEmail is the identifier used for the user inside Xray (logs / user management / stats).
func (u *User) XrayEmail() string { return fmt.Sprintf("u%d", u.ID) }

// ParseXrayEmail is the inverse of XrayEmail.
func ParseXrayEmail(s string) (int64, bool) {
	var id int64
	if _, err := fmt.Sscanf(s, "u%d", &id); err != nil || fmt.Sprintf("u%d", id) != s {
		return 0, false
	}
	return id, true
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: serialize writers
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrations[i] upgrades the schema from version i to i+1 (PRAGMA user_version).
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		email         TEXT    NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT    NOT NULL,
		uuid          TEXT    NOT NULL UNIQUE,
		disabled      INTEGER NOT NULL DEFAULT 0,
		expires_at    INTEGER,
		created_at    INTEGER NOT NULL
	);`,
	`ALTER TABLE users ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE users ADD COLUMN period_start INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE users ADD COLUMN traffic_used INTEGER NOT NULL DEFAULT 0;
	UPDATE users SET period_start = created_at;
	CREATE TABLE orders (
		id           TEXT    PRIMARY KEY,
		user_id      INTEGER NOT NULL REFERENCES users(id),
		plan_id      TEXT    NOT NULL,
		amount_cents INTEGER NOT NULL,
		method       TEXT    NOT NULL,
		status       TEXT    NOT NULL DEFAULT 'pending',
		trade_no     TEXT    NOT NULL DEFAULT '',
		created_at   INTEGER NOT NULL,
		paid_at      INTEGER
	);
	CREATE INDEX orders_user ON orders(user_id, created_at);
	CREATE TABLE traffic_daily (
		user_id INTEGER NOT NULL REFERENCES users(id),
		day     TEXT    NOT NULL,
		up      INTEGER NOT NULL DEFAULT 0,
		down    INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (user_id, day)
	);`,
	`ALTER TABLE users ADD COLUMN ref_code TEXT NOT NULL DEFAULT '';
	ALTER TABLE users ADD COLUMN invited_by INTEGER NOT NULL DEFAULT 0;
	CREATE UNIQUE INDEX users_ref_code ON users(ref_code) WHERE ref_code <> '';`,
	`CREATE TABLE settings (
		key        TEXT    PRIMARY KEY,
		value      TEXT    NOT NULL,
		updated_at INTEGER NOT NULL
	);
	ALTER TABLE users ADD COLUMN note TEXT NOT NULL DEFAULT '';
	CREATE INDEX orders_status ON orders(status, created_at);`,
	// Device-based accounts: make email/password optional and add a stable
	// per-device identity. SQLite can't drop NOT NULL in place, so rebuild
	// the users table (FK enforcement is disabled around the migration run;
	// user ids are preserved so orders/traffic references stay valid).
	`CREATE TABLE users_new (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		email         TEXT    UNIQUE COLLATE NOCASE,
		password_hash TEXT    NOT NULL DEFAULT '',
		uuid          TEXT    NOT NULL UNIQUE,
		disabled      INTEGER NOT NULL DEFAULT 0,
		expires_at    INTEGER,
		created_at    INTEGER NOT NULL,
		plan_id       TEXT    NOT NULL DEFAULT '',
		period_start  INTEGER NOT NULL DEFAULT 0,
		traffic_used  INTEGER NOT NULL DEFAULT 0,
		ref_code      TEXT    NOT NULL DEFAULT '',
		invited_by    INTEGER NOT NULL DEFAULT 0,
		note          TEXT    NOT NULL DEFAULT '',
		device_id     TEXT,
		platform      TEXT    NOT NULL DEFAULT ''
	);
	INSERT INTO users_new (id, email, password_hash, uuid, disabled, expires_at, created_at,
		plan_id, period_start, traffic_used, ref_code, invited_by, note)
		SELECT id, email, password_hash, uuid, disabled, expires_at, created_at,
			plan_id, period_start, traffic_used, ref_code, invited_by, note FROM users;
	DROP TABLE users;
	ALTER TABLE users_new RENAME TO users;
	CREATE UNIQUE INDEX users_ref_code ON users(ref_code) WHERE ref_code <> '';
	CREATE UNIQUE INDEX users_device_id ON users(device_id) WHERE device_id IS NOT NULL;`,
	// Multi-device: a device can follow another account's subscription,
	// joined with a short-lived link code shown as text / QR on the primary.
	`ALTER TABLE users ADD COLUMN primary_id INTEGER NOT NULL DEFAULT 0;
	CREATE INDEX users_primary ON users(primary_id) WHERE primary_id <> 0;
	CREATE TABLE link_codes (
		code       TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX link_codes_user ON link_codes(user_id);`,
}

func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v >= len(migrations) {
		return nil
	}
	// Some migrations rebuild tables referenced by foreign keys (orders,
	// traffic_daily -> users). FK enforcement can't be toggled inside a
	// transaction, so disable it for the whole migration run and re-check
	// integrity afterwards. MaxOpenConns is 1, so these pragmas apply to the
	// same connection the migrations use.
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer s.db.Exec(`PRAGMA foreign_keys = ON`)

	for ; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	// Fail loudly if a rebuild left a dangling reference.
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign_key_check failed after migration")
	}
	return rows.Err()
}

const userCols = `id, email, password_hash, uuid, disabled, expires_at, created_at, plan_id, period_start, traffic_used, ref_code, invited_by, note, device_id, platform, primary_id`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u           User
		email       sql.NullString
		deviceID    sql.NullString
		disabled    int
		expiresAt   sql.NullInt64
		createdAt   int64
		periodStart int64
	)
	if err := row.Scan(&u.ID, &email, &u.PasswordHash, &u.UUID, &disabled, &expiresAt, &createdAt,
		&u.PlanID, &periodStart, &u.TrafficUsed, &u.RefCode, &u.InvitedBy, &u.Note, &deviceID, &u.Platform, &u.PrimaryID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Email = email.String
	u.DeviceID = deviceID.String
	u.Disabled = disabled != 0
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0)
		u.ExpiresAt = &t
	}
	u.CreatedAt = time.Unix(createdAt, 0)
	u.PeriodStart = time.Unix(periodStart, 0)
	return &u, nil
}

func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	id, err := NewUUID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ref, err := newRefCode()
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (email, password_hash, uuid, created_at, period_start, ref_code) VALUES (?, ?, ?, ?, ?, ?)`,
		email, passwordHash, id, now.Unix(), now.Unix(), ref)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	uid, _ := res.LastInsertId()
	return s.UserByID(ctx, uid)
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

func (s *Store) UserByDeviceID(ctx context.Context, deviceID string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE device_id = ?`, deviceID))
}

// CreateDeviceUser creates an account tied to a stable device identity, with no
// email or password. platform is informational ("ios"/"android").
func (s *Store) CreateDeviceUser(ctx context.Context, deviceID, platform string) (*User, error) {
	id, err := NewUUID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ref, err := newRefCode()
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (uuid, created_at, period_start, ref_code, device_id, platform) VALUES (?, ?, ?, ?, ?, ?)`,
		id, now.Unix(), now.Unix(), ref, deviceID, platform)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.device_id") {
			return nil, ErrDeviceTaken
		}
		return nil, err
	}
	uid, _ := res.LastInsertId()
	return s.UserByID(ctx, uid)
}

// DeviceUser returns the account for deviceID, creating it on first contact.
// This is the no-login entry point: a device is recognized immediately and,
// because the id is persisted in the keychain / SSAID, survives reinstalls.
func (s *Store) DeviceUser(ctx context.Context, deviceID, platform string) (*User, error) {
	u, err := s.UserByDeviceID(ctx, deviceID)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	u, err = s.CreateDeviceUser(ctx, deviceID, platform)
	if errors.Is(err, ErrDeviceTaken) { // raced with a concurrent first contact
		return s.UserByDeviceID(ctx, deviceID)
	}
	return u, err
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// EnabledUsers returns all users that are not banned. Plan / quota checks are done by the caller.
func (s *Store) EnabledUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE disabled = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) updateByEmail(ctx context.Context, email, set string, args ...any) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET `+set+` WHERE email = ?`, append(args, email)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDisabled enables/disables a user (admin CLI).
func (s *Store) SetDisabled(ctx context.Context, email string, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	return s.updateByEmail(ctx, email, `disabled = ?`, v)
}

// SetExpiry sets the paid-plan expiry (nil = never) (admin CLI).
func (s *Store) SetExpiry(ctx context.Context, email string, t *time.Time) error {
	var v any
	if t != nil {
		v = t.Unix()
	}
	return s.updateByEmail(ctx, email, `expires_at = ?`, v)
}

// ResetTraffic starts a new traffic period now (admin CLI).
func (s *Store) ResetTraffic(ctx context.Context, email string) error {
	return s.updateByEmail(ctx, email, `traffic_used = 0, period_start = ?`, time.Now().Unix())
}

// RotateUUID issues a new VLESS credential, invalidating the old one (e.g. leaked config).
func (s *Store) RotateUUID(ctx context.Context, email string) error {
	id, err := NewUUID()
	if err != nil {
		return err
	}
	return s.updateByEmail(ctx, email, `uuid = ?`, id)
}

// GrantPlan gives a user a paid plan for days. Buying the same active plan
// extends it; a different plan replaces the current one starting now and
// opens a fresh traffic period.
func (s *Store) GrantPlan(ctx context.Context, email, planID string, days int) error {
	u, err := s.UserByEmail(ctx, email)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := grantPlanTx(ctx, tx, u.ID, planID, days, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

func grantPlanTx(ctx context.Context, tx *sql.Tx, userID int64, planID string, days int, now time.Time) error {
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, userID))
	if err != nil {
		return err
	}
	add := time.Duration(days) * 24 * time.Hour
	if u.PaidPlanActive(now) && u.PlanID == planID {
		_, err = tx.ExecContext(ctx, `UPDATE users SET expires_at = ? WHERE id = ?`, u.ExpiresAt.Add(add).Unix(), userID)
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE users SET plan_id = ?, expires_at = ?, traffic_used = 0, period_start = ? WHERE id = ?`,
		planID, now.Add(add).Unix(), now.Unix(), userID)
	return err
}

// ---- traffic ----

type Traffic struct{ Up, Down int64 }

// AddTraffic records usage reported by a node. A user's period counter is
// reset first if their period has ended. Unknown user IDs are ignored.
func (s *Store) AddTraffic(ctx context.Context, usage map[int64]Traffic, now time.Time, period time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	day := now.Format("2006-01-02")
	ps := int64(period / time.Second)
	for id, t := range usage {
		if t.Up < 0 || t.Down < 0 {
			continue
		}
		total := t.Up + t.Down
		res, err := tx.ExecContext(ctx, `
			UPDATE users SET
				traffic_used = CASE WHEN period_start + ? <= ? THEN ? ELSE traffic_used + ? END,
				period_start = CASE WHEN period_start + ? <= ?
				                    THEN period_start + ((? - period_start) / ?) * ? ELSE period_start END
			WHERE id = ?`,
			ps, now.Unix(), total, total,
			ps, now.Unix(), now.Unix(), ps, ps,
			id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO traffic_daily (user_id, day, up, down) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, day) DO UPDATE SET up = up + excluded.up, down = down + excluded.down`,
			id, day, t.Up, t.Down); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type DailyUsage struct {
	Day  string `json:"day"`
	Up   int64  `json:"up"`
	Down int64  `json:"down"`
}

// Usage returns daily usage since the given day (inclusive), oldest first.
func (s *Store) Usage(ctx context.Context, userID int64, sinceDay string) ([]DailyUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, up, down FROM traffic_daily WHERE user_id = ? AND day >= ? ORDER BY day`, userID, sinceDay)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DailyUsage{}
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Day, &d.Up, &d.Down); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- orders ----

type Order struct {
	ID          string     `json:"id"`
	UserID      int64      `json:"-"`
	PlanID      string     `json:"plan_id"`
	AmountCents int64      `json:"amount_cents"`
	Method      string     `json:"method"`
	Status      string     `json:"status"` // pending | paid
	TradeNo     string     `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	PaidAt      *time.Time `json:"paid_at"`
}

const orderCols = `id, user_id, plan_id, amount_cents, method, status, trade_no, created_at, paid_at`

func scanOrder(row interface{ Scan(...any) error }) (*Order, error) {
	var (
		o       Order
		created int64
		paid    sql.NullInt64
	)
	if err := row.Scan(&o.ID, &o.UserID, &o.PlanID, &o.AmountCents, &o.Method, &o.Status, &o.TradeNo, &created, &paid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	o.CreatedAt = time.Unix(created, 0)
	if paid.Valid {
		t := time.Unix(paid.Int64, 0)
		o.PaidAt = &t
	}
	return &o, nil
}

func (s *Store) CreateOrder(ctx context.Context, userID int64, planID string, amountCents int64, method string) (*Order, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	now := time.Now()
	o := &Order{
		ID:     fmt.Sprintf("VP%s%X", now.Format("20060102150405"), b),
		UserID: userID, PlanID: planID, AmountCents: amountCents, Method: method,
		Status: "pending", CreatedAt: now,
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO orders (id, user_id, plan_id, amount_cents, method, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		o.ID, o.UserID, o.PlanID, o.AmountCents, o.Method, now.Unix())
	return o, err
}

func (s *Store) OrderByID(ctx context.Context, id string) (*Order, error) {
	return scanOrder(s.db.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id = ?`, id))
}

func (s *Store) OrdersByUser(ctx context.Context, userID int64, limit int) ([]*Order, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+orderCols+` FROM orders WHERE user_id = ? AND status = 'paid' ORDER BY created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// MarkPaid atomically marks a pending order paid and grants its plan.
// It is idempotent: payment providers retry notifications, and a second call
// for an already-paid order returns (false, nil) without granting again.
func (s *Store) MarkPaid(ctx context.Context, orderID, tradeNo string, durationDays int, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	o, err := scanOrder(tx.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id = ?`, orderID))
	if err != nil {
		return false, err
	}
	if o.Status == "paid" {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET status = 'paid', trade_no = ?, paid_at = ? WHERE id = ?`,
		tradeNo, now.Unix(), orderID); err != nil {
		return false, err
	}
	if err := grantPlanTx(ctx, tx, o.UserID, o.PlanID, durationDays, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// NewUUID returns a random RFC 4122 version 4 UUID.
func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
