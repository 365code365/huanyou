package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestMigrateV5FromV4 simulates an existing production database at schema v4
// (email NOT NULL, with users + orders + traffic referencing users via FKs)
// and verifies the device-account migration preserves all data and FKs.
func TestMigrateV5FromV4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

	// Build the v4 schema using the first four migrations, then seed data.
	raw, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := raw.Exec(migrations[i]); err != nil {
			t.Fatalf("seed migration %d: %v", i, err)
		}
	}
	if _, err := raw.Exec(`PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := raw.Exec(
		`INSERT INTO users (id, email, password_hash, uuid, created_at, period_start, ref_code)
		 VALUES (1, 'a@example.com', 'hash', 'uuid-1', ?, ?, 'REF001')`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(
		`INSERT INTO orders (id, user_id, plan_id, amount_cents, method, created_at)
		 VALUES ('O1', 1, 'pro', 2500, 'mock', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(
		`INSERT INTO traffic_daily (user_id, day, up, down) VALUES (1, '2026-10-29', 100, 200)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	// Re-open through the store, which should run migration 5.
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open/migrate: %v", err)
	}
	defer st.Close()

	var v int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}

	ctx := context.Background()
	// Existing user preserved, with its email intact.
	u, err := st.UserByID(ctx, 1)
	if err != nil {
		t.Fatalf("UserByID(1): %v", err)
	}
	if u.Email != "a@example.com" || u.RefCode != "REF001" {
		t.Fatalf("user not preserved: %+v", u)
	}
	// Orders and traffic survived the FK-bearing table rebuild.
	if o, err := st.OrderByID(ctx, "O1"); err != nil || o.UserID != 1 {
		t.Fatalf("order lost: %v %+v", err, o)
	}
	usage, err := st.Usage(ctx, 1, "2026-01-01")
	if err != nil || len(usage) != 1 || usage[0].Up != 100 {
		t.Fatalf("traffic lost: %v %+v", err, usage)
	}

	// New device account (no email/password) can be created and re-fetched.
	d, err := st.CreateDeviceUser(ctx, "device-abc-123", "android")
	if err != nil {
		t.Fatalf("CreateDeviceUser: %v", err)
	}
	if d.Email != "" || d.DeviceID != "device-abc-123" || d.Platform != "android" {
		t.Fatalf("device user wrong: %+v", d)
	}
	// DeviceUser is idempotent on the same device id.
	again, err := st.DeviceUser(ctx, "device-abc-123", "android")
	if err != nil || again.ID != d.ID {
		t.Fatalf("DeviceUser not idempotent: %v id=%d want=%d", err, again.ID, d.ID)
	}
	// A second device id makes a distinct account.
	d2, err := st.DeviceUser(ctx, "device-xyz-789", "ios")
	if err != nil || d2.ID == d.ID {
		t.Fatalf("second device not distinct: %v", err)
	}
}
