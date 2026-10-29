// Package store persists users in SQLite (pure Go driver, no cgo).
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
	ErrNotFound   = errors.New("not found")
	ErrEmailTaken = errors.New("email already registered")
)

type User struct {
	ID           int64
	Email        string
	PasswordHash string
	UUID         string // VLESS credential, unique per user
	Disabled     bool
	ExpiresAt    *time.Time // nil = never expires
	CreatedAt    time.Time
}

// Active reports whether the user may currently connect.
func (u *User) Active(now time.Time) bool {
	if u.Disabled {
		return false
	}
	return u.ExpiresAt == nil || now.Before(*u.ExpiresAt)
}

// XrayEmail is the identifier used for the user inside Xray (logs / user management).
func (u *User) XrayEmail() string { return fmt.Sprintf("u%d", u.ID) }

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

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	email         TEXT    NOT NULL UNIQUE COLLATE NOCASE,
	password_hash TEXT    NOT NULL,
	uuid          TEXT    NOT NULL UNIQUE,
	disabled      INTEGER NOT NULL DEFAULT 0,
	expires_at    INTEGER,
	created_at    INTEGER NOT NULL
);`)
	return err
}

const userCols = `id, email, password_hash, uuid, disabled, expires_at, created_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var (
		u         User
		disabled  int
		expiresAt sql.NullInt64
		createdAt int64
	)
	if err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.UUID, &disabled, &expiresAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Disabled = disabled != 0
	if expiresAt.Valid {
		t := time.Unix(expiresAt.Int64, 0)
		u.ExpiresAt = &t
	}
	u.CreatedAt = time.Unix(createdAt, 0)
	return &u, nil
}

func (s *Store) CreateUser(ctx context.Context, email, passwordHash string) (*User, error) {
	id, err := NewUUID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (email, password_hash, uuid, created_at) VALUES (?, ?, ?, ?)`,
		email, passwordHash, id, now.Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: users.email") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	uid, _ := res.LastInsertId()
	return &User{ID: uid, Email: email, PasswordHash: passwordHash, UUID: id, CreatedAt: now}, nil
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// ActiveUsers returns every user that is allowed to connect at time now.
func (s *Store) ActiveUsers(ctx context.Context, now time.Time) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE disabled = 0 AND (expires_at IS NULL OR expires_at > ?)`, now.Unix())
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

// SetDisabled enables/disables a user (admin CLI).
func (s *Store) SetDisabled(ctx context.Context, email string, disabled bool) error {
	v := 0
	if disabled {
		v = 1
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE email = ?`, v, email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetExpiry sets the subscription expiry (nil = never) (admin CLI).
func (s *Store) SetExpiry(ctx context.Context, email string, t *time.Time) error {
	var v any
	if t != nil {
		v = t.Unix()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET expires_at = ? WHERE email = ?`, v, email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateUUID issues a new VLESS credential, invalidating the old one (e.g. leaked config).
func (s *Store) RotateUUID(ctx context.Context, email string) error {
	id, err := NewUUID()
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE users SET uuid = ? WHERE email = ?`, id, email)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
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
