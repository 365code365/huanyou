package store

import (
	"context"
	"testing"
	"time"
)

// Half-day invite rewards, and the inviter's extra bonus on the invitee's
// first payment only (a second order or a repeated notification adds nothing).
func TestReferralHalfDaysAndPaidBonus(t *testing.T) {
	ctx := context.Background()
	st := openTestDB(t, DriverSQLite)
	st.PaidReferral = func() PaidReferralRule { return PaidReferralRule{PlanID: "month", Days: 1.5} }
	now := time.Now()
	a, _ := st.CreateUser(ctx, "inviter@example.com", "h")
	b, _ := st.CreateUser(ctx, "invitee@example.com", "h")
	r, err := st.ApplyReferral(ctx, b.ID, a.ID, "month", 0.5, 0.5, 0, now)
	if err != nil || r.InviterDays != 0.5 || r.InviteeDays != 0.5 {
		t.Fatalf("apply: %+v %v", r, err)
	}
	exp := func(id int64) time.Duration {
		u, err := st.UserByID(ctx, id)
		if err != nil || u.ExpiresAt == nil {
			t.Fatalf("user %d: %v", id, err)
		}
		return u.ExpiresAt.Sub(now).Round(time.Hour)
	}
	if got := exp(a.ID); got != 12*time.Hour {
		t.Fatalf("inviter after signup = %v, want 12h", got)
	}
	for i := 0; i < 2; i++ { // two orders; the first one also notified twice
		o, err := st.CreateOrder(ctx, b.ID, "month", 2500, "alipay")
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 2; j++ {
			if _, err := st.MarkPaid(ctx, o.ID, "T", 30, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := exp(a.ID); got != 48*time.Hour { // 0.5 + 1.5 days
		t.Fatalf("inviter after payments = %v, want 48h", got)
	}
}
