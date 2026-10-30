package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Copying data between databases (SQLite ⇄ MySQL ⇄ PostgreSQL).
//
// Rows are matched on each table's unique business key (users.uid, orders.id,
// (user_id, day) …), never on an auto-increment counter, so a sync can be
// re-run any number of times and only changes rows that differ. User ids are
// copied as they are because they are the public recharge IDs (VP-00010).
//
// Incremental runs read rows with updated_at >= since. Deleted rows are found
// by comparing key sets (small tables) or by their user being gone (traffic,
// IP records).

type colKind int

const (
	kInt colKind = iota
	kNullInt
	kText
	kNullText
)

type col struct {
	name string
	kind colKind
}

type tableSpec struct {
	name    string
	cols    []col
	keys    []string // unique business key used for upserts and deletes
	updated string   // column for incremental reads; "" = always copied in full
	byUser  bool     // rows belong to a user (user_id); removed with the user
	big     bool     // too large to diff key sets on incremental runs
	sets    []string // custom upsert assignments (default: overwrite every non-key column)
}

func c(name string, k colKind) col { return col{name, k} }

// syncTables lists tables parents first (foreign keys).
var syncTables = []tableSpec{
	{name: "users", keys: []string{"uid"}, updated: "updated_at", cols: []col{
		c("id", kInt), c("uid", kText), c("email", kNullText), c("password_hash", kText), c("uuid", kText),
		c("disabled", kInt), c("expires_at", kNullInt), c("created_at", kInt), c("plan_id", kText),
		c("period_start", kInt), c("traffic_used", kInt), c("ref_code", kText), c("invited_by", kInt),
		c("note", kText), c("device_id", kNullText), c("platform", kText), c("primary_id", kInt), c("updated_at", kInt),
	}},
	{name: "orders", keys: []string{"id"}, updated: "updated_at", byUser: true, cols: []col{
		c("id", kText), c("user_id", kInt), c("plan_id", kText), c("amount_cents", kInt), c("method", kText),
		c("status", kText), c("trade_no", kText), c("created_at", kInt), c("paid_at", kNullInt), c("updated_at", kInt),
	}},
	{name: "traffic_daily", keys: []string{"user_id", "day"}, updated: "updated_at", byUser: true, big: true, cols: []col{
		c("user_id", kInt), c("day", kText), c("up", kInt), c("down", kInt), c("updated_at", kInt),
	}},
	{name: "user_ips", keys: []string{"user_id", "ip", "source", "node_id"}, updated: "updated_at", byUser: true, big: true, cols: []col{
		c("user_id", kInt), c("ip", kText), c("source", kText), c("node_id", kText), c("hits", kInt),
		c("first_seen", kInt), c("last_seen", kInt), c("updated_at", kInt),
	}},
	{name: "link_codes", keys: []string{"code"}, byUser: true, cols: []col{
		c("code", kText), c("user_id", kInt), c("expires_at", kInt),
	}},
	{name: "risk_rules", keys: []string{"kind", "value"}, updated: "updated_at", cols: []col{
		c("kind", kText), c("value", kText), c("reason", kText), c("expires_at", kNullInt),
		c("created_at", kInt), c("updated_at", kInt),
	}},
	{name: "settings", keys: []string{"skey"}, updated: "updated_at", cols: []col{
		c("skey", kText), c("value", kText), c("updated_at", kInt),
	}},
	{name: "releases", keys: []string{"platform", "build", "abi"}, updated: "updated_at", cols: []col{
		c("id", kInt), c("platform", kText), c("version", kText), c("build", kInt), c("notes", kText),
		c("file", kText), c("url", kText), c("size", kInt), c("sha256", kText), c("bundle_id", kText),
		c("force_update", kInt), c("published", kInt), c("abi", kText), c("created_at", kInt), c("updated_at", kInt),
	}},
	{name: "app_events", keys: []string{"day", "event", "vkey"}, updated: "updated_at", big: true, cols: []col{
		c("day", kText), c("event", kText), c("vkey", kText), c("platform", kText), c("version", kText),
		c("cnt", kInt), c("updated_at", kInt),
	}},
	{name: "crash_reports", keys: []string{"id"}, updated: "updated_at", cols: []col{
		c("id", kText), c("user_id", kInt), c("platform", kText), c("app_version", kText), c("device", kText),
		c("os_version", kText), c("kind", kText), c("message", kText), c("stack", kText), c("cnt", kInt),
		c("users", kInt), c("first_at", kInt), c("last_at", kInt), c("resolved", kInt), c("updated_at", kInt),
	}},
	{name: "lang_packs", keys: []string{"code"}, updated: "updated_at", cols: []col{
		c("code", kText), c("name", kText), c("native_name", kText), c("rtl", kInt), c("enabled", kInt),
		c("version", kInt), c("strings", kText), c("updated_at", kInt), c("sort", kInt),
	}},
}

// Settings keys that describe this database itself and must not be copied.
var localSettings = map[string]bool{settingDBSync: true}

const settingDBSync = "db_sync" // watermarks of previous syncs, per target

type SyncOptions struct {
	Full     bool                      // copy every row and remove rows missing from the source
	Since    int64                     // incremental: rows with updated_at >= Since (ignored when Full)
	Progress func(table string, n int) // optional, called after each batch
}

type TableResult struct {
	Table    string `json:"table"`
	Upserted int    `json:"upserted"`
	Deleted  int    `json:"deleted"`
}

type SyncResult struct {
	StartedAt int64         `json:"started_at"` // use as the next Since
	Tables    []TableResult `json:"tables"`
	Seconds   float64       `json:"seconds"`
}

const syncBatch = 200

// Sync copies src into dst. dst must already have the current schema (it does
// once opened). Safe to run while src is serving traffic; run it again (or
// incrementally) to pick up rows written meanwhile.
func Sync(ctx context.Context, src, dst *Store, o SyncOptions) (*SyncResult, error) {
	start := time.Now()
	res := &SyncResult{StartedAt: start.Unix()}
	results := map[string]*TableResult{}
	for _, t := range syncTables {
		results[t.name] = &TableResult{Table: t.name}
	}

	// 1. Deletions, children before parents.
	goneUsers, err := missingKeys(ctx, src, dst, syncTables[0])
	if err != nil {
		return nil, err
	}
	if len(goneUsers) > 0 {
		ids, err := dst.userIDsByUID(ctx, goneUsers)
		if err != nil {
			return nil, err
		}
		for i := len(syncTables) - 1; i >= 1; i-- {
			if t := syncTables[i]; t.byUser {
				n, err := dst.deleteWhereIn(ctx, t.name, "user_id", ids)
				if err != nil {
					return nil, err
				}
				results[t.name].Deleted += n
			}
		}
		n, err := dst.deleteKeys(ctx, syncTables[0], goneUsers)
		if err != nil {
			return nil, err
		}
		results["users"].Deleted += n
	}
	for i := len(syncTables) - 1; i >= 1; i-- {
		t := syncTables[i]
		if t.big && !o.Full {
			continue // rows of deleted users are already gone; the rest only ever grow
		}
		gone, err := missingKeys(ctx, src, dst, t)
		if err != nil {
			return nil, err
		}
		n, err := dst.deleteKeys(ctx, t, gone)
		if err != nil {
			return nil, err
		}
		results[t.name].Deleted += n
	}

	// 2. Upserts, parents first.
	for _, t := range syncTables {
		since := int64(-1)
		if !o.Full && t.updated != "" {
			since = o.Since
		}
		n, err := copyTable(ctx, src, dst, t, since, o.Progress)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.name, err)
		}
		results[t.name].Upserted += n
	}
	if err := dst.fixSequences(ctx); err != nil {
		return nil, err
	}
	for _, t := range syncTables {
		res.Tables = append(res.Tables, *results[t.name])
	}
	res.Seconds = time.Since(start).Seconds()
	return res, nil
}

func colNames(t tableSpec) []string {
	out := make([]string, len(t.cols))
	for i, c := range t.cols {
		out[i] = c.name
	}
	return out
}

func copyTable(ctx context.Context, src, dst *Store, t tableSpec, since int64, progress func(string, int)) (int, error) {
	names := colNames(t)
	q := "SELECT " + strings.Join(names, ", ") + " FROM " + t.name
	var args []any
	if since >= 0 {
		q += " WHERE " + t.updated + " >= ?"
		args = append(args, since)
	}
	q += " ORDER BY " + strings.Join(t.keys, ", ")
	rows, err := src.db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	total := 0
	batch := make([][]any, 0, syncBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := dst.upsertRows(ctx, t, batch); err != nil {
			return err
		}
		total += len(batch)
		batch = batch[:0]
		if progress != nil {
			progress(t.name, total)
		}
		return nil
	}
	ptrs := make([]any, len(t.cols))
	vals := make([]any, len(t.cols))
	for i := range ptrs {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return total, err
		}
		row := make([]any, len(t.cols))
		skip := false
		for i, c := range t.cols {
			v, err := normalize(vals[i], c.kind)
			if err != nil {
				return total, fmt.Errorf("%s.%s: %w", t.name, c.name, err)
			}
			row[i] = v
		}
		if t.name == "settings" && localSettings[row[0].(string)] {
			skip = true
		}
		if skip {
			continue
		}
		batch = append(batch, row)
		if len(batch) == syncBatch {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return total, err
	}
	return total, flush()
}

// normalize converts driver values ([]byte from MySQL, int64 / string from
// SQLite, …) into the plain Go types every driver accepts.
func normalize(v any, k colKind) (any, error) {
	if v == nil {
		switch k {
		case kNullInt, kNullText:
			return nil, nil
		case kInt:
			return int64(0), nil
		default:
			return "", nil
		}
	}
	switch k {
	case kInt, kNullInt:
		switch x := v.(type) {
		case int64:
			return x, nil
		case int32:
			return int64(x), nil
		case int:
			return int64(x), nil
		case int16:
			return int64(x), nil
		case float64:
			return int64(x), nil
		case []byte:
			return strconv.ParseInt(string(x), 10, 64)
		case string:
			return strconv.ParseInt(x, 10, 64)
		case bool:
			return int64(b2i(x)), nil
		}
	default:
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case []byte:
			s = string(x)
		case int64:
			s = strconv.FormatInt(x, 10)
		default:
			return nil, fmt.Errorf("unexpected value %T", v)
		}
		// Nullable text columns (email, device_id) are unique: "" means
		// "none" and must be NULL, or a second empty value would collide.
		if k == kNullText && s == "" {
			return nil, nil
		}
		return s, nil
	}
	return nil, fmt.Errorf("unexpected value %T", v)
}

func (s *Store) upsertRows(ctx context.Context, t tableSpec, rows [][]any) error {
	names := colNames(t)
	single := s.d.upsertAll(t.name, names, t.keys)
	if t.name == "users" {
		// id is copied on insert but never rewritten on an existing row.
		var sets []string
		for _, n := range names {
			if n != "uid" && n != "id" {
				sets = append(sets, n+" = NEW("+n+")")
			}
		}
		single = s.d.upsert(t.name, names, t.keys, sets...)
	}
	q, args := multiRow(single, len(names), rows)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return err
	}
	return tx.Commit()
}

// multiRow expands a single-row INSERT … VALUES (?, …) statement to n rows.
func multiRow(single string, ncols int, rows [][]any) (string, []any) {
	one := "(" + strings.TrimSuffix(strings.Repeat("?, ", ncols), ", ") + ")"
	i := strings.Index(single, one)
	if i < 0 || len(rows) == 1 {
		return single, rows[0]
	}
	all := make([]string, len(rows))
	args := make([]any, 0, len(rows)*ncols)
	for j, r := range rows {
		all[j] = one
		args = append(args, r...)
	}
	return single[:i] + strings.Join(all, ", ") + single[i+len(one):], args
}

// keySet reads every key of a table as a joined string.
func (s *Store) keySet(ctx context.Context, t tableSpec) (map[string][]any, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+strings.Join(t.keys, ", ")+" FROM "+t.name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]any{}
	vals := make([]any, len(t.keys))
	ptrs := make([]any, len(t.keys))
	for i := range ptrs {
		ptrs[i] = &vals[i]
	}
	kinds := map[string]colKind{}
	for _, c := range t.cols {
		kinds[c.name] = c.kind
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		key := make([]any, len(t.keys))
		parts := make([]string, len(t.keys))
		for i, k := range t.keys {
			v, err := normalize(vals[i], kinds[k])
			if err != nil {
				return nil, err
			}
			key[i] = v
			parts[i] = fmt.Sprint(v)
		}
		out[strings.Join(parts, "\x00")] = key
	}
	return out, rows.Err()
}

// missingKeys returns keys present in dst but not in src.
func missingKeys(ctx context.Context, src, dst *Store, t tableSpec) ([][]any, error) {
	a, err := src.keySet(ctx, t)
	if err != nil {
		return nil, err
	}
	b, err := dst.keySet(ctx, t)
	if err != nil {
		return nil, err
	}
	var out [][]any
	for k, v := range b {
		if _, ok := a[k]; !ok {
			if t.name == "settings" && localSettings[v[0].(string)] {
				continue
			}
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *Store) deleteKeys(ctx context.Context, t tableSpec, keys [][]any) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	conds := make([]string, len(t.keys))
	for i, k := range t.keys {
		conds[i] = k + " = ?"
	}
	q := "DELETE FROM " + t.name + " WHERE " + strings.Join(conds, " AND ")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n := 0
	for _, k := range keys {
		r, err := tx.ExecContext(ctx, q, k...)
		if err != nil {
			return n, err
		}
		c, _ := r.RowsAffected()
		n += int(c)
	}
	return n, tx.Commit()
}

func (s *Store) deleteWhereIn(ctx context.Context, table, column string, ids []int64) (int, error) {
	n := 0
	for len(ids) > 0 {
		chunk := ids[:min(len(ids), 500)]
		ids = ids[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		r, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+column+" IN ("+
			strings.TrimSuffix(strings.Repeat("?, ", len(chunk)), ", ")+")", args...)
		if err != nil {
			return n, err
		}
		c, _ := r.RowsAffected()
		n += int(c)
	}
	return n, nil
}

func (s *Store) userIDsByUID(ctx context.Context, uids [][]any) ([]int64, error) {
	var out []int64
	for _, k := range uids {
		var id int64
		err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE uid = ?`, k[0]).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// fixSequences moves PostgreSQL identity counters past copied ids (MySQL and
// SQLite advance their counters on explicit inserts by themselves).
func (s *Store) fixSequences(ctx context.Context) error {
	if !s.d.pg() {
		return nil
	}
	for _, t := range []string{"users", "releases"} {
		if _, err := s.db.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence('`+t+`', 'id'),
			COALESCE((SELECT MAX(id) FROM `+t+`), 1), (SELECT MAX(id) FROM `+t+`) IS NOT NULL)`); err != nil {
			return err
		}
	}
	return nil
}

// TableCounts returns the row count of every synced table.
func (s *Store) TableCounts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range syncTables {
		var n int64
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t.name).Scan(&n); err != nil {
			return nil, err
		}
		out[t.name] = n
	}
	return out, nil
}

// ---- sync watermarks (stored in the source database, not copied) ----

// SyncMark records the last successful sync to a target.
type SyncMark struct {
	At      int64         `json:"at"`      // StartedAt of the last run
	Full    bool          `json:"full"`    // last run was a full copy
	Seconds float64       `json:"seconds"` // duration
	Result  []TableResult `json:"result"`
}

func (s *Store) SyncMarks(ctx context.Context) (map[string]SyncMark, error) {
	m := map[string]SyncMark{}
	_, err := s.Setting(ctx, settingDBSync, &m)
	return m, err
}

func (s *Store) PutSyncMark(ctx context.Context, target string, mark SyncMark) error {
	m, err := s.SyncMarks(ctx)
	if err != nil {
		m = map[string]SyncMark{}
	}
	m[target] = mark
	return s.PutSetting(ctx, settingDBSync, m)
}
