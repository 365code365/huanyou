package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Schema.
//
// Every table has a natural or random unique key that does not depend on an
// auto-increment counter, so rows can be copied and incrementally synced
// between databases (see sync.go):
//
//	users          uid (random, never changes; id is the public recharge ID and is copied as is)
//	orders         id ("VP…" order number)
//	traffic_daily  (user_id, day)
//	user_ips       (user_id, ip, source, node_id)
//	risk_rules     (kind, value)
//	link_codes     code
//	releases       (platform, build, abi)
//	lang_packs     code
//	settings       skey
//
// Mutable tables carry updated_at (unix seconds) for incremental sync.
//
// SQLite keeps its step-by-step history (sqliteMigrations, PRAGMA
// user_version). MySQL and PostgreSQL start directly at the current version
// from remoteSchema; later changes are appended to all three.

// sqliteMigrations[i] upgrades the schema from version i to i+1.
var sqliteMigrations = []migration{
	{sql: `CREATE TABLE IF NOT EXISTS users (
		id            INTEGER PRIMARY KEY AUTOINCREMENT,
		email         TEXT    NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT    NOT NULL,
		uuid          TEXT    NOT NULL UNIQUE,
		disabled      INTEGER NOT NULL DEFAULT 0,
		expires_at    INTEGER,
		created_at    INTEGER NOT NULL
	);`},
	{sql: `ALTER TABLE users ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
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
	);`},
	{sql: `ALTER TABLE users ADD COLUMN ref_code TEXT NOT NULL DEFAULT '';
	ALTER TABLE users ADD COLUMN invited_by INTEGER NOT NULL DEFAULT 0;
	CREATE UNIQUE INDEX users_ref_code ON users(ref_code) WHERE ref_code <> '';`},
	{sql: `CREATE TABLE settings (
		key        TEXT    PRIMARY KEY,
		value      TEXT    NOT NULL,
		updated_at INTEGER NOT NULL
	);
	ALTER TABLE users ADD COLUMN note TEXT NOT NULL DEFAULT '';
	CREATE INDEX orders_status ON orders(status, created_at);`},
	// Device-based accounts: make email/password optional and add a stable
	// per-device identity. SQLite can't drop NOT NULL in place, so rebuild
	// the users table (FK enforcement is disabled around the migration run;
	// user ids are preserved so orders/traffic references stay valid).
	{sql: `CREATE TABLE users_new (
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
	CREATE UNIQUE INDEX users_device_id ON users(device_id) WHERE device_id IS NOT NULL;`},
	// Multi-device: a device can follow another account's subscription,
	// joined with a short-lived link code shown as text / QR on the primary.
	{sql: `ALTER TABLE users ADD COLUMN primary_id INTEGER NOT NULL DEFAULT 0;
	CREATE INDEX users_primary ON users(primary_id) WHERE primary_id <> 0;
	CREATE TABLE link_codes (
		code       TEXT    PRIMARY KEY,
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX link_codes_user ON link_codes(user_id);`},
	// App releases for in-app update checks and the download site.
	{sql: `CREATE TABLE releases (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		platform    TEXT    NOT NULL,
		version     TEXT    NOT NULL,
		build       INTEGER NOT NULL,
		notes       TEXT    NOT NULL DEFAULT '',
		file        TEXT    NOT NULL DEFAULT '',
		url         TEXT    NOT NULL DEFAULT '',
		size        INTEGER NOT NULL DEFAULT 0,
		sha256      TEXT    NOT NULL DEFAULT '',
		bundle_id   TEXT    NOT NULL DEFAULT '',
		force       INTEGER NOT NULL DEFAULT 0,
		published   INTEGER NOT NULL DEFAULT 1,
		created_at  INTEGER NOT NULL
	);
	CREATE UNIQUE INDEX releases_platform_build ON releases(platform, build);`},
	// Downloadable app language packs (Chinese is built into the app).
	{sql: `CREATE TABLE lang_packs (
		code        TEXT    PRIMARY KEY,
		name        TEXT    NOT NULL DEFAULT '',
		native_name TEXT    NOT NULL DEFAULT '',
		rtl         INTEGER NOT NULL DEFAULT 0,
		enabled     INTEGER NOT NULL DEFAULT 1,
		version     INTEGER NOT NULL,
		strings     TEXT    NOT NULL DEFAULT '{}',
		updated_at  INTEGER NOT NULL,
		sort        INTEGER NOT NULL DEFAULT 0
	);`},
	// v8 → v9: portable identities and sync columns, MySQL-safe column names,
	// indexes for the admin / dashboard queries, IP records and risk rules.
	{sql: `ALTER TABLE users ADD COLUMN uid TEXT NOT NULL DEFAULT '';
	ALTER TABLE users ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
	UPDATE users SET updated_at = created_at;
	ALTER TABLE orders ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
	UPDATE orders SET updated_at = COALESCE(paid_at, created_at);
	ALTER TABLE traffic_daily ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE releases RENAME COLUMN "force" TO force_update;
	ALTER TABLE releases ADD COLUMN abi TEXT NOT NULL DEFAULT '';
	DROP INDEX releases_platform_build;
	CREATE UNIQUE INDEX releases_platform_build ON releases(platform, build, abi);
	ALTER TABLE releases ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
	UPDATE releases SET updated_at = created_at;
	ALTER TABLE settings RENAME COLUMN "key" TO skey;
	DROP INDEX orders_user;
	CREATE INDEX orders_user ON orders(user_id, status, created_at);
	CREATE INDEX orders_paid ON orders(status, paid_at);
	CREATE INDEX orders_updated ON orders(updated_at);
	CREATE INDEX users_created ON users(created_at);
	CREATE INDEX users_plan_exp ON users(plan_id, expires_at);
	CREATE INDEX users_invited ON users(invited_by) WHERE invited_by <> 0;
	CREATE INDEX users_updated ON users(updated_at);
	CREATE INDEX traffic_day ON traffic_daily(day);
	CREATE INDEX traffic_updated ON traffic_daily(updated_at);
	CREATE TABLE user_ips (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		ip         TEXT    NOT NULL,
		source     TEXT    NOT NULL,
		node_id    TEXT    NOT NULL DEFAULT '',
		hits       INTEGER NOT NULL DEFAULT 0,
		first_seen INTEGER NOT NULL,
		last_seen  INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (user_id, ip, source, node_id)
	);
	CREATE INDEX user_ips_ip ON user_ips(ip, last_seen);
	CREATE INDEX user_ips_seen ON user_ips(last_seen);
	CREATE INDEX user_ips_user ON user_ips(user_id, last_seen);
	CREATE TABLE risk_rules (
		kind       TEXT    NOT NULL,
		value      TEXT    NOT NULL,
		reason     TEXT    NOT NULL DEFAULT '',
		expires_at INTEGER,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (kind, value)
	);`, post: backfillUserUIDs},
}

// SchemaVersion is the current schema version of all dialects.
var SchemaVersion = len(sqliteMigrations)

type migration struct {
	sql  string
	post func(ctx context.Context, tx *txx) error // runs after sql, same transaction
}

// backfillUserUIDs gives every existing user a stable random uid, then makes it unique.
func backfillUserUIDs(ctx context.Context, tx *txx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM users WHERE uid = ''`)
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
		uid, err := NewUUID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET uid = ? WHERE id = ?`, uid, id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE UNIQUE INDEX users_uid ON users(uid)`)
	return err
}

// remoteSchema is the schema at SchemaVersion for MySQL (8.0.19+) and
// PostgreSQL (12+). Statements are separated by ";\n".
var remoteSchema = map[string]string{
	DriverMySQL: `
CREATE TABLE IF NOT EXISTS users (
	id            BIGINT       NOT NULL AUTO_INCREMENT PRIMARY KEY,
	uid           VARCHAR(36)  NOT NULL,
	email         VARCHAR(254) NULL,
	password_hash VARCHAR(100) NOT NULL DEFAULT '',
	uuid          VARCHAR(36)  NOT NULL,
	disabled      SMALLINT     NOT NULL DEFAULT 0,
	expires_at    BIGINT       NULL,
	created_at    BIGINT       NOT NULL,
	plan_id       VARCHAR(64)  NOT NULL DEFAULT '',
	period_start  BIGINT       NOT NULL DEFAULT 0,
	traffic_used  BIGINT       NOT NULL DEFAULT 0,
	ref_code      VARCHAR(16)  NOT NULL,
	invited_by    BIGINT       NOT NULL DEFAULT 0,
	note          VARCHAR(2000) NOT NULL DEFAULT '',
	device_id     VARCHAR(128) NULL,
	platform      VARCHAR(16)  NOT NULL DEFAULT '',
	primary_id    BIGINT       NOT NULL DEFAULT 0,
	updated_at    BIGINT       NOT NULL DEFAULT 0,
	UNIQUE KEY users_uid (uid),
	UNIQUE KEY users_email (email),
	UNIQUE KEY users_uuid (uuid),
	UNIQUE KEY users_ref_code (ref_code),
	UNIQUE KEY users_device_id (device_id),
	KEY users_primary (primary_id),
	KEY users_created (created_at),
	KEY users_plan_exp (plan_id, expires_at),
	KEY users_invited (invited_by),
	KEY users_updated (updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS orders (
	id           VARCHAR(40)  NOT NULL PRIMARY KEY,
	user_id      BIGINT       NOT NULL,
	plan_id      VARCHAR(64)  NOT NULL,
	amount_cents BIGINT       NOT NULL,
	method       VARCHAR(32)  NOT NULL,
	status       VARCHAR(16)  NOT NULL DEFAULT 'pending',
	trade_no     VARCHAR(128) NOT NULL DEFAULT '',
	created_at   BIGINT       NOT NULL,
	paid_at      BIGINT       NULL,
	updated_at   BIGINT       NOT NULL DEFAULT 0,
	KEY orders_user (user_id, status, created_at),
	KEY orders_status (status, created_at),
	KEY orders_paid (status, paid_at),
	KEY orders_updated (updated_at),
	CONSTRAINT orders_user_fk FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS traffic_daily (
	user_id    BIGINT   NOT NULL,
	day        CHAR(10) NOT NULL,
	up         BIGINT   NOT NULL DEFAULT 0,
	down       BIGINT   NOT NULL DEFAULT 0,
	updated_at BIGINT   NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, day),
	KEY traffic_day (day),
	KEY traffic_updated (updated_at),
	CONSTRAINT traffic_user_fk FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS settings (
	skey       VARCHAR(64) NOT NULL PRIMARY KEY,
	value      MEDIUMTEXT  NOT NULL,
	updated_at BIGINT      NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS link_codes (
	code       VARCHAR(16) NOT NULL PRIMARY KEY,
	user_id    BIGINT      NOT NULL,
	expires_at BIGINT      NOT NULL,
	KEY link_codes_user (user_id),
	CONSTRAINT link_codes_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS releases (
	id           BIGINT        NOT NULL AUTO_INCREMENT PRIMARY KEY,
	platform     VARCHAR(16)   NOT NULL,
	version      VARCHAR(32)   NOT NULL,
	build        BIGINT        NOT NULL,
	notes        TEXT          NOT NULL,
	file         VARCHAR(255)  NOT NULL DEFAULT '',
	url          VARCHAR(1024) NOT NULL DEFAULT '',
	size         BIGINT        NOT NULL DEFAULT 0,
	sha256       VARCHAR(64)   NOT NULL DEFAULT '',
	bundle_id    VARCHAR(255)  NOT NULL DEFAULT '',
	force_update SMALLINT      NOT NULL DEFAULT 0,
	published    SMALLINT      NOT NULL DEFAULT 1,
	abi          VARCHAR(16)   NOT NULL DEFAULT '',
	created_at   BIGINT        NOT NULL,
	updated_at   BIGINT        NOT NULL DEFAULT 0,
	UNIQUE KEY releases_platform_build (platform, build, abi)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS lang_packs (
	code        VARCHAR(16)  NOT NULL PRIMARY KEY,
	name        VARCHAR(64)  NOT NULL DEFAULT '',
	native_name VARCHAR(128) NOT NULL DEFAULT '',
	rtl         SMALLINT     NOT NULL DEFAULT 0,
	enabled     SMALLINT     NOT NULL DEFAULT 1,
	version     BIGINT       NOT NULL,
	strings     MEDIUMTEXT   NOT NULL,
	updated_at  BIGINT       NOT NULL,
	sort        INT          NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS user_ips (
	user_id    BIGINT      NOT NULL,
	ip         VARCHAR(45) NOT NULL,
	source     VARCHAR(8)  NOT NULL,
	node_id    VARCHAR(64) NOT NULL DEFAULT '',
	hits       BIGINT      NOT NULL DEFAULT 0,
	first_seen BIGINT      NOT NULL,
	last_seen  BIGINT      NOT NULL,
	updated_at BIGINT      NOT NULL,
	PRIMARY KEY (user_id, ip, source, node_id),
	KEY user_ips_ip (ip, last_seen),
	KEY user_ips_seen (last_seen),
	KEY user_ips_user (user_id, last_seen),
	CONSTRAINT user_ips_user_fk FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS risk_rules (
	kind       VARCHAR(8)   NOT NULL,
	value      VARCHAR(64)  NOT NULL,
	reason     VARCHAR(500) NOT NULL DEFAULT '',
	expires_at BIGINT       NULL,
	created_at BIGINT       NOT NULL,
	updated_at BIGINT       NOT NULL,
	PRIMARY KEY (kind, value)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin`,

	DriverPostgres: `
CREATE TABLE IF NOT EXISTS users (
	id            BIGINT  GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
	uid           TEXT    NOT NULL UNIQUE,
	email         TEXT    UNIQUE,
	password_hash TEXT    NOT NULL DEFAULT '',
	uuid          TEXT    NOT NULL UNIQUE,
	disabled      SMALLINT NOT NULL DEFAULT 0,
	expires_at    BIGINT,
	created_at    BIGINT  NOT NULL,
	plan_id       TEXT    NOT NULL DEFAULT '',
	period_start  BIGINT  NOT NULL DEFAULT 0,
	traffic_used  BIGINT  NOT NULL DEFAULT 0,
	ref_code      TEXT    NOT NULL,
	invited_by    BIGINT  NOT NULL DEFAULT 0,
	note          TEXT    NOT NULL DEFAULT '',
	device_id     TEXT    UNIQUE,
	platform      TEXT    NOT NULL DEFAULT '',
	primary_id    BIGINT  NOT NULL DEFAULT 0,
	updated_at    BIGINT  NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS users_ref_code ON users(ref_code) WHERE ref_code <> '';
CREATE INDEX IF NOT EXISTS users_primary ON users(primary_id) WHERE primary_id <> 0;
CREATE INDEX IF NOT EXISTS users_created ON users(created_at);
CREATE INDEX IF NOT EXISTS users_plan_exp ON users(plan_id, expires_at);
CREATE INDEX IF NOT EXISTS users_invited ON users(invited_by) WHERE invited_by <> 0;
CREATE INDEX IF NOT EXISTS users_updated ON users(updated_at);
CREATE TABLE IF NOT EXISTS orders (
	id           TEXT   PRIMARY KEY,
	user_id      BIGINT NOT NULL REFERENCES users(id),
	plan_id      TEXT   NOT NULL,
	amount_cents BIGINT NOT NULL,
	method       TEXT   NOT NULL,
	status       TEXT   NOT NULL DEFAULT 'pending',
	trade_no     TEXT   NOT NULL DEFAULT '',
	created_at   BIGINT NOT NULL,
	paid_at      BIGINT,
	updated_at   BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS orders_user ON orders(user_id, status, created_at);
CREATE INDEX IF NOT EXISTS orders_status ON orders(status, created_at);
CREATE INDEX IF NOT EXISTS orders_paid ON orders(status, paid_at);
CREATE INDEX IF NOT EXISTS orders_updated ON orders(updated_at);
CREATE TABLE IF NOT EXISTS traffic_daily (
	user_id    BIGINT NOT NULL REFERENCES users(id),
	day        TEXT   NOT NULL,
	up         BIGINT NOT NULL DEFAULT 0,
	down       BIGINT NOT NULL DEFAULT 0,
	updated_at BIGINT NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, day)
);
CREATE INDEX IF NOT EXISTS traffic_day ON traffic_daily(day);
CREATE INDEX IF NOT EXISTS traffic_updated ON traffic_daily(updated_at);
CREATE TABLE IF NOT EXISTS settings (
	skey       TEXT   PRIMARY KEY,
	value      TEXT   NOT NULL,
	updated_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS link_codes (
	code       TEXT   PRIMARY KEY,
	user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS link_codes_user ON link_codes(user_id);
CREATE TABLE IF NOT EXISTS releases (
	id           BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
	platform     TEXT   NOT NULL,
	version      TEXT   NOT NULL,
	build        BIGINT NOT NULL,
	notes        TEXT   NOT NULL DEFAULT '',
	file         TEXT   NOT NULL DEFAULT '',
	url          TEXT   NOT NULL DEFAULT '',
	size         BIGINT NOT NULL DEFAULT 0,
	sha256       TEXT   NOT NULL DEFAULT '',
	bundle_id    TEXT   NOT NULL DEFAULT '',
	force_update SMALLINT NOT NULL DEFAULT 0,
	published    SMALLINT NOT NULL DEFAULT 1,
	abi          TEXT   NOT NULL DEFAULT '',
	created_at   BIGINT NOT NULL,
	updated_at   BIGINT NOT NULL DEFAULT 0,
	UNIQUE (platform, build, abi)
);
CREATE TABLE IF NOT EXISTS lang_packs (
	code        TEXT     PRIMARY KEY,
	name        TEXT     NOT NULL DEFAULT '',
	native_name TEXT     NOT NULL DEFAULT '',
	rtl         SMALLINT NOT NULL DEFAULT 0,
	enabled     SMALLINT NOT NULL DEFAULT 1,
	version     BIGINT   NOT NULL,
	strings     TEXT     NOT NULL DEFAULT '{}',
	updated_at  BIGINT   NOT NULL,
	sort        INT      NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS user_ips (
	user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	ip         TEXT   NOT NULL,
	source     TEXT   NOT NULL,
	node_id    TEXT   NOT NULL DEFAULT '',
	hits       BIGINT NOT NULL DEFAULT 0,
	first_seen BIGINT NOT NULL,
	last_seen  BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (user_id, ip, source, node_id)
);
CREATE INDEX IF NOT EXISTS user_ips_ip ON user_ips(ip, last_seen);
CREATE INDEX IF NOT EXISTS user_ips_seen ON user_ips(last_seen);
CREATE INDEX IF NOT EXISTS user_ips_user ON user_ips(user_id, last_seen);
CREATE TABLE IF NOT EXISTS risk_rules (
	kind       TEXT   NOT NULL,
	value      TEXT   NOT NULL,
	reason     TEXT   NOT NULL DEFAULT '',
	expires_at BIGINT,
	created_at BIGINT NOT NULL,
	updated_at BIGINT NOT NULL,
	PRIMARY KEY (kind, value)
)`,
}

func (s *Store) migrate(ctx context.Context) error {
	if s.d.name == DriverSQLite {
		return s.migrateSQLite(ctx)
	}
	return s.migrateRemote(ctx)
}

func (s *Store) migrateSQLite(ctx context.Context) error {
	// One dedicated connection: the foreign_keys pragma is per connection and
	// can't be toggled inside a transaction, and some migrations rebuild
	// tables referenced by foreign keys.
	conn, err := s.db.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var v int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v > len(sqliteMigrations) {
		return fmt.Errorf("数据库版本 %d 比本程序（%d）新，请升级程序", v, len(sqliteMigrations))
	}
	if v == len(sqliteMigrations) {
		return nil
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)

	for ; v < len(sqliteMigrations); v++ {
		m := sqliteMigrations[v]
		raw, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		tx := &txx{Tx: raw, d: s.d}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", v+1, err)
		}
		if m.post != nil {
			if err := m.post(ctx, tx); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", v+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}

	// Fail loudly if a rebuild left a dangling reference.
	rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign_key_check failed after migration")
	}
	return rows.Err()
}

func (s *Store) migrateRemote(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INT NOT NULL)`); err != nil {
		return err
	}
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		v = 0
	case err != nil:
		return err
	}
	if v > SchemaVersion {
		return fmt.Errorf("数据库版本 %d 比本程序（%d）新，请升级程序", v, SchemaVersion)
	}
	if v == SchemaVersion {
		return nil
	}
	if v != 0 {
		// Place for future remote migrations (v → v+1 for each dialect).
		return fmt.Errorf("不支持从数据库版本 %d 升级", v)
	}
	// Fresh database: create everything at the current version. DDL is not
	// transactional on MySQL; every statement is idempotent so a failed run
	// can simply be retried.
	for _, stmt := range strings.Split(remoteSchema[s.d.name], ";\n") {
		if stmt = strings.TrimSpace(stmt); stmt == "" {
			continue
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create schema: %w\n%s", err, firstLine(stmt))
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, SchemaVersion)
	return err
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
