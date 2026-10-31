package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// A database at the current version that ran an earlier draft of migration 9
// (no releases.abi, old unique index) is repaired at open.
func TestRepairDraftSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`DROP INDEX releases_platform_build`,
		`ALTER TABLE releases DROP COLUMN abi`,
		`CREATE UNIQUE INDEX releases_platform_build ON releases(platform, build)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	for _, abi := range []string{"arm64-v8a", "armeabi-v7a"} {
		if _, err := st.CreateRelease(ctx, &Release{Platform: "android", Version: "1.2.5", Build: 9, URL: "https://x/a.apk", ABI: abi, Published: true}); err != nil {
			t.Fatalf("release %s after repair: %v", abi, err)
		}
	}
	if r, err := st.LatestRelease(ctx, "android", "armeabi-v7a"); err != nil || r.ABI != "armeabi-v7a" {
		t.Fatalf("latest: %+v %v", r, err)
	}
	// Second open: nothing left to repair, still fine.
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()
}

// users.uid missing at the current version: the repair adds it and gives
// every existing user its own uid (sync and backups key users on it).
func TestRepairMissingUserUID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, dev := range []string{"and-repair-0001", "and-repair-0002"} {
		if _, err := st.DeviceUser(ctx, dev, "android"); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP INDEX users_uid`, `ALTER TABLE users DROP COLUMN uid`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.db.QueryContext(ctx, `SELECT uid FROM users`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if uid == "" || seen[uid] {
			t.Fatalf("uid %q empty or repeated", uid)
		}
		seen[uid] = true
	}
	if len(seen) != 2 {
		t.Fatalf("users after repair: %d", len(seen))
	}
}
