package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Multi-device sharing.
//
// A "primary" account with an eligible plan creates a short link code. Another
// device redeems it and becomes a "member": it keeps its own identity (UUID,
// recharge ID) but follows the primary's subscription and shares its traffic
// quota. Members can't be primaries, so there are no chains.

var (
	ErrDeviceLimit  = errors.New("device limit reached")
	ErrSelfLink     = errors.New("cannot link to self")
	ErrCodeInvalid  = errors.New("link code invalid or expired")
	ErrHasMembers   = errors.New("device has members")
	ErrPrimaryIsMem = errors.New("primary is itself a member")
)

// NormalizeLinkCode uppercases and strips separators: "abcd-efgh" -> "ABCDEFGH".
func NormalizeLinkCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, s)
}

// FormatLinkCode renders "ABCDEFGH" as "ABCD-EFGH" for display.
func FormatLinkCode(c string) string {
	if len(c) == 8 {
		return c[:4] + "-" + c[4:]
	}
	return c
}

// CreateLinkCode replaces any code the user has with a new one valid for ttl.
func (s *Store) CreateLinkCode(ctx context.Context, userID int64, ttl time.Duration) (string, time.Time, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	code := make([]byte, 8)
	for i, v := range b {
		code[i] = refAlphabet[int(v)%len(refAlphabet)]
	}
	exp := time.Now().Add(ttl)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM link_codes WHERE user_id = ? OR expires_at < ?`, userID, time.Now().Unix()); err != nil {
		return "", time.Time{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO link_codes (code, user_id, expires_at) VALUES (?, ?, ?)`,
		string(code), userID, exp.Unix()); err != nil {
		return "", time.Time{}, err
	}
	return string(code), exp, tx.Commit()
}

// RevokeLinkCodes removes all of a user's link codes.
func (s *Store) RevokeLinkCodes(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM link_codes WHERE user_id = ?`, userID)
	return err
}

// ActiveLinkCode returns the user's unexpired code, if any.
func (s *Store) ActiveLinkCode(ctx context.Context, userID int64) (string, time.Time, bool) {
	var code string
	var exp int64
	err := s.db.QueryRowContext(ctx, `SELECT code, expires_at FROM link_codes WHERE user_id = ? AND expires_at > ?`,
		userID, time.Now().Unix()).Scan(&code, &exp)
	if err != nil {
		return "", time.Time{}, false
	}
	return code, time.Unix(exp, 0), true
}

// LinkCodeOwner returns the primary user id for a valid, unexpired code.
func (s *Store) LinkCodeOwner(ctx context.Context, code string) (int64, error) {
	var uid int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM link_codes WHERE code = ? AND expires_at > ?`,
		NormalizeLinkCode(code), time.Now().Unix()).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrCodeInvalid
	}
	return uid, err
}

// Members returns the devices following primaryID, oldest first.
func (s *Store) Members(ctx context.Context, primaryID int64) ([]*User, error) {
	return scanUsers(s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE primary_id = ? ORDER BY id`, primaryID))
}

// LinkDevice makes memberID follow primaryID, allowing at most maxMembers
// followers. Idempotent if already linked to the same primary.
func (s *Store) LinkDevice(ctx context.Context, memberID, primaryID int64, maxMembers int) error {
	if memberID == primaryID {
		return ErrSelfLink
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Lock the primary's row so two devices joining at once can't both pass the
	// member limit (SQLite transactions already hold the write lock).
	lock := ""
	if s.d.name != DriverSQLite {
		lock = " FOR UPDATE"
	}
	var primaryOf int64
	if err := tx.QueryRowContext(ctx, `SELECT primary_id FROM users WHERE id = ?`+lock, primaryID).Scan(&primaryOf); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if primaryOf != 0 {
		return ErrPrimaryIsMem
	}
	var cur int64
	if err := tx.QueryRowContext(ctx, `SELECT primary_id FROM users WHERE id = ?`, memberID).Scan(&cur); err != nil {
		return err
	}
	if cur == primaryID {
		return tx.Commit()
	}
	var followers, count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE primary_id = ?`, memberID).Scan(&followers); err != nil {
		return err
	}
	if followers > 0 {
		return ErrHasMembers
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE primary_id = ?`, primaryID).Scan(&count); err != nil {
		return err
	}
	if count >= maxMembers {
		return ErrDeviceLimit
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET primary_id = ?, updated_at = ? WHERE id = ?`, primaryID, time.Now().Unix(), memberID); err != nil {
		return err
	}
	return tx.Commit()
}

// UnlinkDevice detaches a member (it falls back to its own plan).
func (s *Store) UnlinkDevice(ctx context.Context, memberID int64) error {
	return s.updateByID(ctx, memberID, `primary_id = 0`)
}

// RevokeMember detaches memberID only if it currently follows primaryID.
func (s *Store) RevokeMember(ctx context.Context, primaryID, memberID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE users SET primary_id = 0, updated_at = ? WHERE id = ? AND primary_id = ?`,
		time.Now().Unix(), memberID, primaryID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FoldToOwners re-keys per-user usage so a member's traffic counts against
// its owner's quota (multi-device accounts share one quota).
func (s *Store) FoldToOwners(ctx context.Context, usage map[int64]Traffic) (map[int64]Traffic, error) {
	if len(usage) == 0 {
		return usage, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, primary_id FROM users WHERE primary_id <> 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owner := map[int64]int64{}
	for rows.Next() {
		var id, p int64
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		owner[id] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[int64]Traffic, len(usage))
	for id, t := range usage {
		if p, ok := owner[id]; ok {
			id = p
		}
		cur := out[id]
		out[id] = Traffic{Up: cur.Up + t.Up, Down: cur.Down + t.Down}
	}
	return out, nil
}
