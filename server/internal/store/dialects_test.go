package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Runs the same store operations on every configured database, then copies
// data SQLite → PostgreSQL → MySQL → SQLite and checks it arrives intact.
//
//	VPN_TEST_MYSQL='root:pass@tcp(127.0.0.1:3306)/vpn'
//	VPN_TEST_PG='postgres://postgres:pass@127.0.0.1:5432/vpn?sslmode=disable'
//
// Both databases are wiped (all tables dropped) by the test.

func openTestDB(t *testing.T, driver string) *Store {
	t.Helper()
	switch driver {
	case DriverSQLite:
		st, err := Open(filepath.Join(t.TempDir(), "t.db"))
		if err != nil {
			t.Fatal(err)
		}
		return st
	case DriverMySQL, DriverPostgres:
		env := map[string]string{DriverMySQL: "VPN_TEST_MYSQL", DriverPostgres: "VPN_TEST_PG"}[driver]
		dsn := os.Getenv(env)
		if dsn == "" {
			t.Skipf("%s not set", env)
		}
		wipe(t, driver, dsn)
		st, err := OpenDSN(driver, dsn)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	t.Fatal("unknown driver")
	return nil
}

func wipe(t *testing.T, driver, dsn string) {
	drv := map[string]string{DriverMySQL: "mysql", DriverPostgres: "pgx"}[driver]
	db, err := sql.Open(drv, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables := []string{"crash_reports", "app_events", "risk_rules", "user_ips", "lang_packs", "releases", "link_codes", "settings",
		"traffic_daily", "orders", "users", "schema_version"}
	for _, tb := range tables {
		q := "DROP TABLE IF EXISTS " + tb
		if driver == DriverPostgres {
			q += " CASCADE"
		}
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("wipe %s: %v", tb, err)
		}
	}
}

func TestStoreOnAllDatabases(t *testing.T) {
	for _, drv := range []string{DriverSQLite, DriverMySQL, DriverPostgres} {
		t.Run(drv, func(t *testing.T) {
			st := openTestDB(t, drv)
			defer st.Close()
			exercise(t, st)
		})
	}
}

// exercise touches every query path that has dialect-specific SQL.
func exercise(t *testing.T, st *Store) {
	ctx := context.Background()
	now := time.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	a, err := st.CreateUser(ctx, "Alice_X@Example.com", "hash")
	must(err)
	if a.Email != "alice_x@example.com" || a.UID == "" || a.RefCode == "" {
		t.Fatalf("user: %+v", a)
	}
	if _, err := st.CreateUser(ctx, "alice_x@example.com", "h"); err != ErrEmailTaken {
		t.Fatalf("duplicate email: %v", err)
	}
	d1, err := st.DeviceUser(ctx, "and-device-0001", "android")
	must(err)
	d2, err := st.DeviceUser(ctx, "and-device-0002", "android")
	must(err)
	again, err := st.DeviceUser(ctx, "and-device-0001", "android")
	must(err)
	if again.ID != d1.ID {
		t.Fatal("device user not idempotent")
	}
	if _, err := st.CreateDeviceUser(ctx, "and-device-0001", "android"); err != ErrDeviceTaken {
		t.Fatalf("duplicate device: %v", err)
	}

	// Orders + idempotent payment + plan grant.
	o, err := st.CreateOrder(ctx, a.ID, "month", 2500, "alipay")
	must(err)
	ok, err := st.MarkPaid(ctx, o.ID, "T123", 30, now)
	must(err)
	if !ok {
		t.Fatal("first MarkPaid should apply")
	}
	if ok, _ := st.MarkPaid(ctx, o.ID, "T123", 30, now); ok {
		t.Fatal("second MarkPaid must be a no-op")
	}
	a, _ = st.UserByID(ctx, a.ID)
	if !a.PaidPlanActive(now) || a.PlanID != "month" {
		t.Fatalf("plan not granted: %+v", a)
	}
	must(st.GrantPlanByID(ctx, a.ID, "month", 10))

	// Traffic with period rollover arithmetic (integer division).
	must(st.AddTraffic(ctx, map[int64]Traffic{a.ID: {100, 200}, d1.ID: {5, 5}}, now, 30*24*time.Hour))
	must(st.AddTraffic(ctx, map[int64]Traffic{a.ID: {1, 2}}, now, 30*24*time.Hour))
	a, _ = st.UserByID(ctx, a.ID)
	if a.TrafficUsed != 303 {
		t.Fatalf("traffic_used = %d", a.TrafficUsed)
	}
	// Force a rollover: period ended long ago.
	_, err = st.db.ExecContext(ctx, `UPDATE users SET period_start = ? WHERE id = ?`, now.Add(-95*24*time.Hour).Unix(), d1.ID)
	must(err)
	must(st.AddTraffic(ctx, map[int64]Traffic{d1.ID: {7, 0}}, now, 30*24*time.Hour))
	d1, _ = st.UserByID(ctx, d1.ID)
	if d1.TrafficUsed != 7 || d1.PeriodStart.After(now) || now.Sub(d1.PeriodStart) >= 30*24*time.Hour {
		t.Fatalf("rollover wrong: used=%d start=%v", d1.TrafficUsed, d1.PeriodStart)
	}
	usage, err := st.Usage(ctx, a.ID, "2000-01-01")
	must(err)
	if len(usage) != 1 || usage[0].Up != 101 || usage[0].Down != 202 {
		t.Fatalf("usage: %+v", usage)
	}

	// Admin queries.
	stats, err := st.AdminStats(ctx, now)
	must(err)
	if stats.Users != 3 || stats.RevenueCents != 2500 || stats.Orders != 1 {
		t.Fatalf("stats: %+v", stats)
	}
	trend, err := st.Trend(ctx, now, 7)
	must(err)
	last := trend[len(trend)-1]
	if last.NewUsers != 3 || last.RevenueCents != 2500 || last.TrafficBytes != 320 {
		t.Fatalf("trend today: %+v", last)
	}
	list, total, err := st.ListUsers(ctx, UserFilter{Query: "ALICE_x", Limit: 10})
	must(err)
	if total != 1 || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("search by email: total=%d", total)
	}
	if _, total, _ := st.ListUsers(ctx, UserFilter{Query: "100%", Limit: 10}); total != 0 {
		t.Fatal("LIKE wildcard not escaped")
	}
	if _, total, _ := st.ListUsers(ctx, UserFilter{Status: "paid", Limit: 10}); total != 1 {
		t.Fatalf("paid filter: %d", total)
	}
	orders, total, err := st.ListOrders(ctx, OrderFilter{Query: strings.ToLower(o.ID[:10]), Limit: 10})
	must(err)
	if total != 1 || orders[0].Email != a.Email {
		t.Fatalf("order search: %d", total)
	}

	// Multi-device.
	code, _, err := st.CreateLinkCode(ctx, a.ID, time.Hour)
	must(err)
	owner, err := st.LinkCodeOwner(ctx, code)
	must(err)
	if owner != a.ID {
		t.Fatal("link code owner")
	}
	must(st.LinkDevice(ctx, d2.ID, a.ID, 5))
	if err := st.LinkDevice(ctx, d1.ID, d2.ID, 5); err != ErrPrimaryIsMem {
		t.Fatalf("chain allowed: %v", err)
	}
	m, err := st.Members(ctx, a.ID)
	must(err)
	if len(m) != 1 {
		t.Fatal("members")
	}

	// Referral.
	r, err := st.ApplyReferral(ctx, d1.ID, a.ID, "month", 3, 3, 0, now)
	must(err)
	if r.InviteeDays != 3 || r.InviterDays != 3 {
		t.Fatalf("referral: %+v", r)
	}
	if n, _ := st.CountInvited(ctx, a.ID); n != 1 {
		t.Fatal("invited count")
	}

	// Settings, catalog-style upsert, language packs, releases.
	must(st.PutSetting(ctx, "k1", map[string]int{"a": 1}))
	must(st.PutSetting(ctx, "k1", map[string]int{"a": 2}))
	var got map[string]int
	if ok, err := st.Setting(ctx, "k1", &got); !ok || err != nil || got["a"] != 2 {
		t.Fatalf("setting: %v %v %v", ok, err, got)
	}
	p1, err := st.SaveLangPack(ctx, &LangPack{Code: "en", NativeName: "English", Enabled: true, Strings: map[string]string{"连接": "Connect"}})
	must(err)
	p2, err := st.SaveLangPack(ctx, &LangPack{Code: "en", NativeName: "English", Enabled: true, Strings: map[string]string{"连接": "Go"}})
	must(err)
	if p2.Version <= p1.Version || p2.Strings["连接"] != "Go" {
		t.Fatalf("lang pack version %d -> %d", p1.Version, p2.Version)
	}
	rel, err := st.CreateRelease(ctx, &Release{Platform: "android", Version: "1.0.0", Build: 1, Published: true, Force: true})
	must(err)
	if rel.ID == 0 || !rel.Force {
		t.Fatalf("release: %+v", rel)
	}
	if _, err := st.CreateRelease(ctx, &Release{Platform: "android", Version: "1.0.0", Build: 1}); err != ErrBuildExists {
		t.Fatalf("duplicate build: %v", err)
	}
	if forced, _ := st.ForcedSince(ctx, "android", 0); !forced {
		t.Fatal("forced release")
	}
	must(st.UpdateRelease(ctx, rel.ID, "notes", false, true, ""))
	if _, err := st.CreateRelease(ctx, &Release{Platform: "android", Version: "1.0.0", Build: 1, ABI: "armeabi-v7a", Published: true}); err != nil {
		t.Fatalf("same build, other abi: %v", err)
	}
	if l, err := st.LatestRelease(ctx, "android", "armeabi-v7a"); err != nil || l.ABI != "armeabi-v7a" {
		t.Fatalf("latest for armeabi-v7a: %v %+v", err, l)
	}
	if l, err := st.LatestRelease(ctx, "android", ""); err != nil || l.ABI != "" {
		t.Fatalf("latest for unknown abi: %v %+v", err, l)
	}

	// IP records and risk rules.
	must(st.RecordIPs(ctx, []IPHit{
		{UserID: a.ID, IP: "1.2.3.4", Source: "api", Hits: 2, At: now.Unix()},
		{UserID: a.ID, IP: "1.2.3.4", Source: "api", Hits: 1, At: now.Unix() + 5},
		{UserID: d1.ID, IP: "1.2.3.4", Source: "node", NodeID: "us1", Hits: 1, At: now.Unix()},
	}))
	ips, err := st.UserIPs(ctx, IPFilter{UserID: a.ID, Limit: 10})
	must(err)
	if len(ips.Items) != 1 || ips.Items[0].Hits != 3 {
		t.Fatalf("ip records: %+v", ips.Items)
	}
	must(st.PutRiskRule(ctx, RiskRule{Kind: "ip", Value: "10.0.0.0/8", Reason: "test"}))
	must(st.PutRiskRule(ctx, RiskRule{Kind: "ip", Value: "10.0.0.0/8", Reason: "updated"}))
	rules, err := st.RiskRules(ctx)
	must(err)
	if len(rules) != 1 || rules[0].Reason != "updated" {
		t.Fatalf("rules: %+v", rules)
	}
	sus, err := st.SharedIPs(ctx, now.Add(-time.Hour).Unix(), 2, 10)
	must(err)
	if len(sus) != 1 || sus[0].Users != 2 {
		t.Fatalf("shared ips: %+v", sus)
	}
	facts, err := st.UserFacts(ctx, now)
	must(err)
	if len(facts) != 3 {
		t.Fatalf("facts: %d", len(facts))
	}
	for _, f := range facts {
		if f.ID == a.ID && (f.LTVCents != 2500 || f.PaidOrders != 1 || f.Invites != 1 || f.Bytes30 != 303) {
			t.Fatalf("facts for a: %+v", f)
		}
	}

	// Analytics and crashes.
	today := now.Format("2006-01-02")
	must(st.RecordEvents(ctx, []EventHit{
		{Day: today, Event: "app_open", VKey: "u1", Platform: "android", Version: "1.2.1", Count: 2},
		{Day: today, Event: "app_open", VKey: "u1", Platform: "android", Version: "1.2.1", Count: 1},
		{Day: today, Event: "app_open", VKey: "dabc", Platform: "ios", Count: 1},
		{Day: today, Event: "vpn_connected", VKey: "u1", Count: 4},
	}))
	ds, err := st.DailyStats(ctx, now, 7)
	must(err)
	if l := ds[len(ds)-1]; l.UV != 2 || l.Opens != 4 || l.Connects != 4 {
		t.Fatalf("daily stats: %+v", l)
	}
	cid := CrashID("1.2.1", "dart", "boom", "a\nb")
	must(st.RecordCrash(ctx, Crash{ID: cid, Kind: "dart", Message: "boom", Stack: "a\nb", AppVersion: "1.2.1"}, true))
	must(st.RecordCrash(ctx, Crash{ID: cid, Kind: "dart", Message: "boom", Stack: "a\nb", AppVersion: "1.2.1"}, false))
	cr, err := st.Crashes(ctx, false, 10)
	must(err)
	if len(cr) != 1 || cr[0].Count != 2 || cr[0].Users != 1 {
		t.Fatalf("crashes: %+v", cr)
	}
	must(st.SetCrashResolved(ctx, cid, true))

	// Delete a user with children everywhere.
	must(st.DeleteUser(ctx, d1.ID))
	if _, err := st.UserByID(ctx, d1.ID); err != ErrNotFound {
		t.Fatal("user not deleted")
	}
}

func TestSyncAcrossDatabases(t *testing.T) {
	ctx := context.Background()
	src := openTestDB(t, DriverSQLite)
	defer src.Close()
	exercise(t, src)

	chain := []*Store{src}
	for _, drv := range []string{DriverPostgres, DriverMySQL} {
		if os.Getenv(map[string]string{DriverMySQL: "VPN_TEST_MYSQL", DriverPostgres: "VPN_TEST_PG"}[drv]) == "" {
			continue
		}
		st := openTestDB(t, drv)
		defer st.Close()
		chain = append(chain, st)
	}
	back := openTestDB(t, DriverSQLite)
	defer back.Close()
	chain = append(chain, back)

	for i := 1; i < len(chain); i++ {
		if _, err := Sync(ctx, chain[i-1], chain[i], SyncOptions{Full: true}); err != nil {
			t.Fatalf("sync %s -> %s: %v", chain[i-1].Driver(), chain[i].Driver(), err)
		}
		assertSame(t, chain[0], chain[i])
	}

	// Incremental: change, add and delete on the source, then sync only changes.
	last := chain[len(chain)-1]
	mark := time.Now().Unix() - 1
	u, err := src.DeviceUser(ctx, "and-device-0099", "android")
	if err != nil {
		t.Fatal(err)
	}
	if err := src.SetNoteByID(ctx, u.ID, "vip"); err != nil {
		t.Fatal(err)
	}
	list, _, _ := src.ListUsers(ctx, UserFilter{Query: "alice", Limit: 1})
	if err := src.DeleteUser(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(chain); i++ {
		res, err := Sync(ctx, chain[i-1], chain[i], SyncOptions{Since: mark})
		if err != nil {
			t.Fatalf("incremental %s -> %s: %v", chain[i-1].Driver(), chain[i].Driver(), err)
		}
		if res.Tables[0].Upserted == 0 || res.Tables[0].Deleted != 1 {
			t.Fatalf("incremental users: %+v", res.Tables[0])
		}
	}
	assertSame(t, src, last)

	// New ids continue after copied ones (sequences moved).
	for _, st := range chain[1:] {
		n, err := st.DeviceUser(ctx, "and-device-new-"+st.Driver(), "ios")
		if err != nil {
			t.Fatalf("%s: insert after sync: %v", st.Driver(), err)
		}
		if n.ID <= u.ID {
			t.Fatalf("%s: new id %d not after copied max %d", st.Driver(), n.ID, u.ID)
		}
	}
}

// assertSame compares every synced table row by row.
func assertSame(t *testing.T, a, b *Store) {
	t.Helper()
	ctx := context.Background()
	for _, tb := range syncTables {
		ra, err := dump(ctx, a, tb)
		if err != nil {
			t.Fatal(err)
		}
		rb, err := dump(ctx, b, tb)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ra, rb) {
			t.Fatalf("%s differs between %s and %s:\n%v\n%v", tb.name, a.Driver(), b.Driver(), ra, rb)
		}
	}
}

func dump(ctx context.Context, s *Store, t tableSpec) ([][]any, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+strings.Join(colNames(t), ", ")+" FROM "+t.name+" ORDER BY "+strings.Join(t.keys, ", "))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(t.cols))
		ptrs := make([]any, len(t.cols))
		for i := range ptrs {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(t.cols))
		for i, c := range t.cols {
			v, err := normalize(vals[i], c.kind)
			if err != nil {
				return nil, err
			}
			if t.name == "settings" && i == 0 && localSettings[v.(string)] {
				row = nil
				break
			}
			row[i] = v
		}
		if row != nil {
			out = append(out, row)
		}
	}
	return out, rows.Err()
}

// TestRemoteUpgrade: a MySQL / PostgreSQL database created by the previous
// version (schema v9) is upgraded in place on open.
func TestRemoteUpgrade(t *testing.T) {
	for _, drv := range []string{DriverMySQL, DriverPostgres} {
		env := map[string]string{DriverMySQL: "VPN_TEST_MYSQL", DriverPostgres: "VPN_TEST_PG"}[drv]
		dsn := os.Getenv(env)
		if dsn == "" {
			continue
		}
		wipe(t, drv, dsn)
		db, err := sql.Open(map[string]string{DriverMySQL: "mysql", DriverPostgres: "pgx"}[drv], dsn)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range strings.Split(remoteSchema[drv], ";\n") {
			if strings.TrimSpace(stmt) != "" {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
		}
		db.Exec(`CREATE TABLE schema_version (version INT NOT NULL)`)
		db.Exec(`INSERT INTO schema_version (version) VALUES (9)`)
		db.Close()
		st, err := OpenDSN(drv, dsn)
		if err != nil {
			t.Fatalf("%s upgrade: %v", drv, err)
		}
		if _, err := st.Crashes(context.Background(), true, 1); err != nil {
			t.Fatalf("%s crash table: %v", drv, err)
		}
		st.Close()
	}
}
