// Package store persists users, orders, traffic and settings. SQLite (pure Go,
// no cgo) is the default; MySQL 8.0.19+ and PostgreSQL 12+ are supported for
// larger deployments (see dialect.go, schema.go, sync.go).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("not found")
	ErrEmailTaken  = errors.New("email already registered")
	ErrDeviceTaken = errors.New("device already registered")
)

type User struct {
	ID           int64
	UID          string // stable random identity used for syncing between databases
	Email        string // "" for device-only accounts (no email/password)
	PasswordHash string
	UUID         string // VLESS credential, unique per user (rotates)
	Disabled     bool   // admin ban, overrides everything
	PlanID       string // paid plan; "" = none
	ExpiresAt    *time.Time
	PeriodStart  time.Time // start of the current traffic period
	TrafficUsed  int64     // bytes (up+down) in the current period
	RefCode      string    // this user's own invite code
	InvitedBy    int64     // user id of the inviter, 0 = none
	Note         string    // admin note
	DeviceID     string    // stable per-device identity (keychain / SSAID); "" if none
	Platform     string    // "ios" | "android" | "windows" | "macos" | "" — informational
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

type Store struct {
	db   *dbx
	d    dialect
	path string // SQLite file ("" for MySQL / PostgreSQL)
	// PaidReferral, when set, returns the current "invitee paid" bonus rule;
	// MarkPaid applies it on a user's first paid order.
	PaidReferral func() PaidReferralRule
}

// Driver returns "sqlite", "mysql" or "postgres".
func (s *Store) Driver() string { return s.d.name }

// Open opens (creating if needed) the SQLite database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	// WAL lets readers run alongside the writer; synchronous=NORMAL is safe
	// with WAL and avoids an fsync per commit; _txlock=immediate takes the
	// write lock at BEGIN so concurrent transactions queue on busy_timeout
	// instead of failing with SQLITE_BUSY when they upgrade from read to write.
	dsn := "file:" + path + "?_txlock=immediate" +
		"&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)&_pragma=cache_size(-16000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Several connections so admin / dashboard reads don't queue behind each
	// other or behind traffic writes (SQLite still serializes the writers).
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return open(db, dialect{DriverSQLite}, path)
}

// OpenDSN opens a MySQL or PostgreSQL database and creates / upgrades its schema.
//
//	mysql:    user:pass@tcp(host:3306)/vpn
//	postgres: postgres://user:pass@host:5432/vpn?sslmode=disable
func OpenDSN(driver, dsn string) (*Store, error) {
	var drv string
	switch driver {
	case DriverMySQL:
		drv = "mysql"
		dsn = mysqlDSN(dsn)
	case DriverPostgres:
		drv = "pgx"
	case DriverSQLite:
		return Open(dsn)
	default:
		return nil, fmt.Errorf("不支持的数据库类型 %q", driver)
	}
	db, err := sql.Open(drv, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(32)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库失败：%w", err)
	}
	return open(db, dialect{driver}, "")
}

// mysqlDSN adds the options this package relies on: utf8mb4 and statement
// interpolation off (server-side prepared statements keep types exact).
func mysqlDSN(dsn string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	if !strings.Contains(dsn, "charset=") {
		dsn += sep + "charset=utf8mb4"
		sep = "&"
	}
	if !strings.Contains(dsn, "timeout=") {
		dsn += sep + "timeout=10s"
	}
	// Whatever the admin pasted: no LOAD DATA LOCAL (a hostile MySQL server
	// could otherwise read files of this machine), no cleartext auth, and
	// read / write timeouts so a half-open connection can't hang a request.
	if c, err := mysql.ParseDSN(dsn); err == nil {
		c.AllowAllFiles = false
		c.AllowCleartextPasswords = false
		if c.ReadTimeout == 0 {
			c.ReadTimeout = 60 * time.Second
		}
		if c.WriteTimeout == 0 {
			c.WriteTimeout = 60 * time.Second
		}
		dsn = c.FormatDSN()
	}
	return dsn
}

func open(db *sql.DB, d dialect, path string) (*Store, error) {
	s := &Store{db: &dbx{DB: db, d: d}, d: d, path: path}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Ping checks the connection.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

const userCols = `id, uid, email, password_hash, uuid, disabled, expires_at, created_at, plan_id, period_start, traffic_used, ref_code, invited_by, note, device_id, platform, primary_id`

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
	if err := row.Scan(&u.ID, &u.UID, &email, &u.PasswordHash, &u.UUID, &disabled, &expiresAt, &createdAt,
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

func scanUsers(rows *sql.Rows, err error) ([]*User, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// newUserIDs returns a fresh uid, VLESS uuid and invite code.
func newUserIDs() (uid, vless, ref string, err error) {
	if uid, err = NewUUID(); err != nil {
		return
	}
	if vless, err = NewUUID(); err != nil {
		return
	}
	ref, err = newRefCode()
	return
}

func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	for attempt := 0; ; attempt++ {
		uid, vless, ref, err := newUserIDs()
		if err != nil {
			return nil, err
		}
		now := time.Now().Unix()
		id, err := insertID(ctx, s.db, s.d,
			`INSERT INTO users (uid, email, password_hash, uuid, created_at, period_start, ref_code, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uid, email, passwordHash, vless, now, now, ref, now)
		if isUnique(err) {
			if _, e := s.UserByEmail(ctx, email); e == nil {
				return nil, ErrEmailTaken
			}
			if attempt < 3 { // random invite code collided; try another
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		return s.UserByID(ctx, id)
	}
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`,
		strings.ToLower(strings.TrimSpace(email))))
}

func (s *Store) UserByDeviceID(ctx context.Context, deviceID string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE device_id = ?`, deviceID))
}

// CreateDeviceUser creates an account tied to a stable device identity, with no
// email or password. platform is informational ("ios"/"android"/"windows"/"macos").
func (s *Store) CreateDeviceUser(ctx context.Context, deviceID, platform string) (*User, error) {
	for attempt := 0; ; attempt++ {
		uid, vless, ref, err := newUserIDs()
		if err != nil {
			return nil, err
		}
		now := time.Now().Unix()
		id, err := insertID(ctx, s.db, s.d,
			`INSERT INTO users (uid, uuid, created_at, period_start, ref_code, device_id, platform, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			uid, vless, now, now, ref, deviceID, platform, now)
		if isUnique(err) {
			if _, e := s.UserByDeviceID(ctx, deviceID); e == nil {
				return nil, ErrDeviceTaken
			}
			if attempt < 3 {
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		return s.UserByID(ctx, id)
	}
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
	return scanUsers(s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE disabled = 0`))
}

func (s *Store) updateByEmail(ctx context.Context, email, set string, args ...any) error {
	args = append(args, time.Now().Unix(), strings.ToLower(strings.TrimSpace(email)))
	res, err := s.db.ExecContext(ctx, `UPDATE users SET `+set+`, updated_at = ? WHERE email = ?`, args...)
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
	return s.updateByEmail(ctx, email, `disabled = ?`, b2i(disabled))
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

// lockUser reads a user inside a transaction, locking the row on MySQL /
// PostgreSQL (SQLite transactions already hold the write lock).
func lockUser(ctx context.Context, tx *txx, id int64) (*User, error) {
	q := `SELECT ` + userCols + ` FROM users WHERE id = ?`
	if tx.d.name != DriverSQLite {
		q += ` FOR UPDATE`
	}
	return scanUser(tx.QueryRowContext(ctx, q, id))
}

func grantPlanTx(ctx context.Context, tx *txx, userID int64, planID string, days int, now time.Time) error {
	return grantPlanForTx(ctx, tx, userID, planID, time.Duration(days)*24*time.Hour, now)
}

// grantPlanForTx is grantPlanTx for any length (e.g. half a day).
func grantPlanForTx(ctx context.Context, tx *txx, userID int64, planID string, add time.Duration, now time.Time) error {
	u, err := lockUser(ctx, tx, userID)
	if err != nil {
		return err
	}
	if u.PaidPlanActive(now) && u.PlanID == planID {
		_, err = tx.ExecContext(ctx, `UPDATE users SET expires_at = ?, updated_at = ? WHERE id = ?`,
			u.ExpiresAt.Add(add).Unix(), now.Unix(), userID)
		return err
	}
	if u.PaidPlanActive(now) {
		// Switching to another plan while one is active: the time already
		// paid for is kept (added on top), never thrown away.
		_, err = tx.ExecContext(ctx,
			`UPDATE users SET plan_id = ?, expires_at = ?, traffic_used = 0, period_start = ?, updated_at = ? WHERE id = ?`,
			planID, u.ExpiresAt.Add(add).Unix(), now.Unix(), now.Unix(), userID)
		return err
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE users SET plan_id = ?, expires_at = ?, traffic_used = 0, period_start = ?, updated_at = ? WHERE id = ?`,
		planID, now.Add(add).Unix(), now.Unix(), now.Unix(), userID)
	return err
}

// ---- traffic ----

type Traffic struct{ Up, Down int64 }

// MaxTrafficDelta caps one user's traffic in one report (1 PiB): anything
// larger is a broken or malicious agent, and summing such values could
// overflow int64 (or turn into a REAL in SQLite and break every user read).
const MaxTrafficDelta int64 = 1 << 50

// TrafficBatch identifies one traffic report of a node. A report the node
// sends again (its earlier attempt was committed but the response was lost)
// is then not counted twice. A zero value disables the check (old agents).
type TrafficBatch struct{ Node, ID string }

const batchHistory = 64 // batch ids remembered per node

// AddTraffic records usage reported by a node. A user's period counter is
// reset first if their period has ended. Unknown user IDs are ignored.
// applied is false when batch was already recorded (nothing changed).
func (s *Store) AddTraffic(ctx context.Context, usage map[int64]Traffic, now time.Time, period time.Duration, batch TrafficBatch) (applied bool, err error) {
	if len(usage) == 0 && batch.ID == "" {
		return true, nil
	}
	// A batch with no usage (only source addresses) still goes through the
	// history: the caller records its IP hits only when it was applied.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if batch.ID != "" {
		// The history lives in the same transaction as the counters: either
		// both are written or neither, so a retry is recognised even after
		// an API restart.
		key := "traffic_batches:" + batch.Node
		q := `SELECT value FROM settings WHERE skey = ?`
		if s.d.name != DriverSQLite {
			q += ` FOR UPDATE`
		}
		var raw string
		if err := tx.QueryRowContext(ctx, q, key).Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		var ids []string
		_ = json.Unmarshal([]byte(raw), &ids)
		if slices.Contains(ids, batch.ID) {
			return false, nil
		}
		ids = append(ids, batch.ID)
		if len(ids) > batchHistory {
			ids = ids[len(ids)-batchHistory:]
		}
		b, _ := json.Marshal(ids)
		up := s.d.upsert("settings", []string{"skey", "value", "updated_at"}, []string{"skey"},
			"value = NEW(value)", "updated_at = NEW(updated_at)")
		if _, err := tx.ExecContext(ctx, up, key, string(b), now.Unix()); err != nil {
			return false, err
		}
	}
	day := now.Format("2006-01-02")
	ps, ts := int64(period/time.Second), now.Unix()
	updUser := `
		UPDATE users SET
			traffic_used = CASE WHEN period_start + ? <= ? THEN ? ELSE traffic_used + ? END,
			period_start = CASE WHEN period_start + ? <= ?
			                    THEN period_start + ` + s.d.idiv("? - period_start", "?") + ` * ? ELSE period_start END,
			updated_at = ?
		WHERE id = ?`
	updDay := s.d.upsert("traffic_daily", []string{"user_id", "day", "up", "down", "updated_at"}, []string{"user_id", "day"},
		"up = traffic_daily.up + NEW(up)", "down = traffic_daily.down + NEW(down)", "updated_at = NEW(updated_at)")
	// Rows are locked in id order, so two nodes reporting at once (or a
	// report and a payment) can't deadlock on MySQL / PostgreSQL.
	ids := make([]int64, 0, len(usage))
	for id := range usage {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		t := usage[id]
		if t.Up < 0 || t.Down < 0 || t.Up > MaxTrafficDelta || t.Down > MaxTrafficDelta {
			continue
		}
		total := t.Up + t.Down
		res, err := tx.ExecContext(ctx, updUser,
			ps, ts, total, total,
			ps, ts, ts, ps, ps,
			ts, id)
		if err != nil {
			return false, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, updDay, id, day, t.Up, t.Down, ts); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
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
		`INSERT INTO orders (id, user_id, plan_id, amount_cents, method, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.UserID, o.PlanID, o.AmountCents, o.Method, now.Unix(), now.Unix())
	return o, err
}

func (s *Store) OrderByID(ctx context.Context, id string) (*Order, error) {
	return scanOrder(s.db.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id = ?`, id))
}

// EnsureOrder creates an order with a caller-supplied id, or returns the
// existing one. Used for App Store purchases, where the Apple transaction id
// is the natural idempotency key (a re-verify or "restore purchases" must not
// create a second order). created is false when the order already existed.
func (s *Store) EnsureOrder(ctx context.Context, id string, userID int64, planID string, amountCents int64, method string) (o *Order, created bool, err error) {
	if o, err = s.OrderByID(ctx, id); err == nil {
		return o, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	now := time.Now()
	o = &Order{ID: id, UserID: userID, PlanID: planID, AmountCents: amountCents, Method: method, Status: "pending", CreatedAt: now}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO orders (id, user_id, plan_id, amount_cents, method, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.UserID, o.PlanID, o.AmountCents, o.Method, now.Unix(), now.Unix())
	if err != nil {
		// A concurrent verify may have inserted it first: treat as existing.
		if o2, e2 := s.OrderByID(ctx, id); e2 == nil {
			return o2, false, nil
		}
		return nil, false, err
	}
	return o, true, nil
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

// PendingOrders returns unpaid orders created between from and to (oldest
// first), for reconciling with payment providers.
func (s *Store) PendingOrders(ctx context.Context, from, to time.Time, limit int) ([]*Order, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+orderCols+` FROM orders
		WHERE status = 'pending' AND created_at >= ? AND created_at <= ? ORDER BY created_at LIMIT ?`,
		from.Unix(), to.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Order
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
	q := `SELECT ` + orderCols + ` FROM orders WHERE id = ?`
	if s.d.name != DriverSQLite {
		q += ` FOR UPDATE` // two notifications at once must not both grant
	}
	o, err := scanOrder(tx.QueryRowContext(ctx, q, orderID))
	if err != nil {
		return false, err
	}
	if o.Status == "paid" {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET status = 'paid', trade_no = ?, paid_at = ?, updated_at = ? WHERE id = ?`,
		tradeNo, now.Unix(), now.Unix(), orderID); err != nil {
		return false, err
	}
	if err := grantPlanTx(ctx, tx, o.UserID, o.PlanID, durationDays, now); err != nil {
		return false, err
	}
	if s.PaidReferral != nil {
		if err := paidReferralTx(ctx, tx, o.UserID, s.PaidReferral(), now); err != nil {
			return false, err
		}
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

// SnapshotTo writes a consistent SQLite copy of the database to path, safe
// while the server is running. For MySQL / PostgreSQL the data is copied
// into a fresh SQLite file, so backups and clones always carry one format.
func (s *Store) SnapshotTo(ctx context.Context, path string) error {
	_ = os.Remove(path)
	if s.d.name == DriverSQLite {
		_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
		return err
	}
	dst, err := Open(path)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = Sync(ctx, s, dst, SyncOptions{Full: true})
	return err
}

// Counts returns row counts for the backup manifest.
func (s *Store) Counts(ctx context.Context) (users, orders int64, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users), (SELECT COUNT(*) FROM orders)`).Scan(&users, &orders)
	return
}

// PendingRestore is where a restored database is staged until the next start.
func PendingRestore(dbPath string) string { return dbPath + ".restore" }

// ApplyPendingRestore swaps in a staged restore before the database is opened:
// the current file is kept as <db>.bak-pre-restore-<time>. Returns true if a
// restore was applied.
func ApplyPendingRestore(dbPath string) (bool, error) {
	staged := PendingRestore(dbPath)
	if _, err := os.Stat(staged); err != nil {
		return false, nil
	}
	if _, err := os.Stat(dbPath); err == nil {
		bak := dbPath + ".bak-pre-restore-" + time.Now().Format("20060102150405")
		if err := os.Rename(dbPath, bak); err != nil {
			return false, err
		}
		// Commits not yet checkpointed live only in the WAL: it goes with the
		// backup (SQLite replays <bak>-wal when the backup is opened), so the
		// kept copy is complete. The -shm index is rebuilt on open.
		if _, err := os.Stat(dbPath + "-wal"); err == nil {
			if err := os.Rename(dbPath+"-wal", bak+"-wal"); err != nil {
				return false, err
			}
		}
	}
	for _, sfx := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + sfx)
	}
	if err := os.Rename(staged, dbPath); err != nil {
		return false, err
	}
	return true, nil
}

// ValidateDB opens a candidate SQLite database file read-only and checks it is
// a healthy database of this system (has the users table, passes integrity_check).
func ValidateDB(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&res); err != nil {
		return fmt.Errorf("数据库无法读取：%w", err)
	}
	if res != "ok" {
		return fmt.Errorf("数据库校验失败：%s", res)
	}
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(sqliteMigrations) {
		return fmt.Errorf("备份来自更新版本的系统（数据库版本 %d，本系统支持 %d），请先升级本系统", v, len(sqliteMigrations))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'`).Scan(&n); err != nil || n == 0 {
		return errors.New("不是本系统的数据库")
	}
	return nil
}
