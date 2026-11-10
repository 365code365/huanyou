package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"
)

// refAlphabet excludes easily confused characters (0/O, 1/I/L).
const refAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newRefCode() (string, error) {
	b := make([]byte, 7)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = refAlphabet[int(v)%len(refAlphabet)]
	}
	return string(out), nil
}

// EnsureRefCodes assigns an invite code to any legacy user missing one.
func (s *Store) EnsureRefCodes(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users WHERE ref_code = ''`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		for attempt := 0; ; attempt++ {
			code, err := newRefCode()
			if err != nil {
				return err
			}
			_, err = s.db.ExecContext(ctx, `UPDATE users SET ref_code = ?, updated_at = ? WHERE id = ?`, code, time.Now().Unix(), id)
			if err == nil {
				break
			}
			// Retry only the rare code collision; anything else (busy, read-only
			// disk, database down) must not spin forever at startup.
			if !isUnique(err) || attempt >= 20 {
				return fmt.Errorf("assign invite code to user %d: %w", id, err)
			}
		}
	}
	return nil
}

func (s *Store) UserByRefCode(ctx context.Context, code string) (*User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE ref_code = ? AND ref_code <> ''`, code))
}

// CountInvited returns how many users were invited by inviterID.
func (s *Store) CountInvited(ctx context.Context, inviterID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE invited_by = ? AND invited_by <> 0`, inviterID).Scan(&n)
	return n, err
}

// ReferralResult reports what rewards an invitation produced.
type ReferralResult struct {
	InviterID    int64
	InviterDays  float64 // 0 if capped out
	InviteeDays  float64
	RewardPlanID string
}

// ApplyReferral links a brand-new user to an inviter and grants the configured
// bonus days to both. inviterDays is skipped when the inviter has hit maxInvites
// (0 = unlimited). It is a no-op if the user was already linked.
func (s *Store) ApplyReferral(ctx context.Context, newUserID, inviterID int64,
	planID string, inviterDays, inviteeDays float64, maxInvites int, now time.Time) (*ReferralResult, error) {

	if newUserID == inviterID {
		return nil, errors.New("cannot invite yourself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Link only if not already linked (prevents double rewards).
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET invited_by = ?, updated_at = ? WHERE id = ? AND invited_by = 0`, inviterID, now.Unix(), newUserID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, errors.New("already linked")
	}

	out := &ReferralResult{InviterID: inviterID, RewardPlanID: planID}
	if inviteeDays > 0 {
		if err := grantBonusTx(ctx, tx, newUserID, planID, inviteeDays, now); err != nil {
			return nil, err
		}
		out.InviteeDays = inviteeDays
	}
	if inviterDays > 0 {
		// Lock the inviter first, so concurrent sign-ups with one code can't
		// all pass the invite cap (SQLite already holds the write lock).
		if _, err := lockUser(ctx, tx, inviterID); err != nil {
			return nil, err
		}
		var invited int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE invited_by = ? AND invited_by <> 0`, inviterID).Scan(&invited); err != nil {
			return nil, err
		}
		if maxInvites == 0 || invited <= maxInvites {
			if err := grantBonusTx(ctx, tx, inviterID, planID, inviterDays, now); err != nil {
				return nil, err
			}
			out.InviterDays = inviterDays
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// grantBonusTx gives reward days: a user with an active paid plan gets them
// on that plan (a bonus must never swap a better plan for the reward plan);
// otherwise the reward plan is granted.
func grantBonusTx(ctx context.Context, tx *txx, userID int64, rewardPlan string, days float64, now time.Time) error {
	u, err := lockUser(ctx, tx, userID)
	if err != nil {
		return err
	}
	add := time.Duration(days * float64(24*time.Hour))
	if u.PaidPlanActive(now) {
		return grantPlanForTx(ctx, tx, userID, u.PlanID, add, now)
	}
	return grantPlanForTx(ctx, tx, userID, rewardPlan, add, now)
}

// PaidReferralRule is the "invitee paid" bonus (Days = 0: off).
type PaidReferralRule struct {
	PlanID     string
	Days       float64
	MaxInvites int // 0 = unlimited
}

// paidReferralTx gives the inviter of userID the paid bonus when this is the
// user's first paid order. Runs inside MarkPaid, so a repeated notification
// for the same order never gets here again.
func paidReferralTx(ctx context.Context, tx *txx, userID int64, r PaidReferralRule, now time.Time) error {
	if r.Days <= 0 || r.PlanID == "" {
		return nil
	}
	var inviter int64
	var paid int
	if err := tx.QueryRowContext(ctx, `SELECT invited_by FROM users WHERE id = ?`, userID).Scan(&inviter); err != nil || inviter == 0 {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE user_id = ? AND status = 'paid'`, userID).Scan(&paid); err != nil || paid != 1 {
		return err
	}
	if r.MaxInvites > 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(DISTINCT o.user_id) FROM orders o JOIN users u ON u.id = o.user_id
			WHERE u.invited_by = ? AND o.status = 'paid'`, inviter).Scan(&n); err != nil {
			return err
		}
		if n > r.MaxInvites {
			return nil
		}
	}
	if _, err := lockUser(ctx, tx, inviter); errors.Is(err, ErrNotFound) {
		return nil // inviter deleted
	} else if err != nil {
		return err
	}
	return grantBonusTx(ctx, tx, inviter, r.PlanID, r.Days, now)
}
