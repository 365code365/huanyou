package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Driver names accepted in the database settings.
const (
	DriverSQLite   = "sqlite"
	DriverMySQL    = "mysql"
	DriverPostgres = "postgres"
)

// dialect hides the SQL differences between the supported databases. Queries
// are written once with "?" placeholders and SQLite/PostgreSQL upsert syntax
// in mind; the few spots that differ go through these helpers.
type dialect struct{ name string }

func (d dialect) pg() bool    { return d.name == DriverPostgres }
func (d dialect) mysql() bool { return d.name == DriverMySQL }

// rebind turns "?" placeholders into $1, $2 … for PostgreSQL. "?" inside
// quoted string literals is left alone.
func (d dialect) rebind(q string) string {
	if !d.pg() || !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 16)
	n, inStr := 0, false
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'':
			inStr = !inStr
			b.WriteByte(c)
		case c == '?' && !inStr:
			n++
			fmt.Fprintf(&b, "$%d", n)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// upsert builds "INSERT … ON CONFLICT (keys) DO UPDATE SET …" (or MySQL's
// ON DUPLICATE KEY UPDATE). In sets, NEW(col) is the incoming value and a bare
// column (qualify it with the table name for PostgreSQL) the current value.
func (d dialect) upsert(table string, cols, keys []string, sets ...string) string {
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	q := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + ph + ")"
	if d.mysql() {
		q += " AS new"
	}
	if len(sets) == 0 {
		if d.mysql() {
			return strings.Replace(q, "INSERT INTO", "INSERT IGNORE INTO", 1)
		}
		return q + " ON CONFLICT (" + strings.Join(keys, ", ") + ") DO NOTHING"
	}
	newRef := "excluded."
	if d.mysql() {
		newRef = "new."
	}
	parts := make([]string, len(sets))
	for i, s := range sets {
		parts[i] = replaceNEW(s, newRef)
	}
	if d.mysql() {
		return q + " ON DUPLICATE KEY UPDATE " + strings.Join(parts, ", ")
	}
	return q + " ON CONFLICT (" + strings.Join(keys, ", ") + ") DO UPDATE SET " + strings.Join(parts, ", ")
}

// upsertAll updates every non-key column from the incoming row.
func (d dialect) upsertAll(table string, cols, keys []string) string {
	isKey := map[string]bool{}
	for _, k := range keys {
		isKey[k] = true
	}
	var sets []string
	for _, c := range cols {
		if !isKey[c] {
			sets = append(sets, c+" = NEW("+c+")")
		}
	}
	return d.upsert(table, cols, keys, sets...)
}

func replaceNEW(s, ref string) string {
	for {
		i := strings.Index(s, "NEW(")
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], ')')
		if j < 0 {
			return s
		}
		s = s[:i] + ref + s[i+4:i+j] + s[i+j+1:]
	}
}

// greatest is the two-argument maximum (SQLite's scalar MAX).
func (d dialect) greatest(a, b string) string {
	if d.name == DriverSQLite {
		return "MAX(" + a + ", " + b + ")"
	}
	return "GREATEST(" + a + ", " + b + ")"
}

// idiv is integer division (MySQL "/" returns a decimal).
func (d dialect) idiv(a, b string) string {
	if d.mysql() {
		return "(" + a + ") DIV (" + b + ")"
	}
	return "(" + a + ") / (" + b + ")"
}

// isUnique reports a unique / primary key violation on any driver.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		c := se.Code()
		return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code == "23505"
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ---- connection wrappers: every query goes through rebind ----

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type dbx struct {
	*sql.DB
	d dialect
}

func (x *dbx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return x.DB.ExecContext(ctx, x.d.rebind(q), args...)
}

func (x *dbx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return x.DB.QueryContext(ctx, x.d.rebind(q), args...)
}

func (x *dbx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return x.DB.QueryRowContext(ctx, x.d.rebind(q), args...)
}

func (x *dbx) BeginTx(ctx context.Context, opts *sql.TxOptions) (*txx, error) {
	tx, err := x.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &txx{Tx: tx, d: x.d}, nil
}

type txx struct {
	*sql.Tx
	d dialect
}

func (x *txx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return x.Tx.ExecContext(ctx, x.d.rebind(q), args...)
}

func (x *txx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return x.Tx.QueryContext(ctx, x.d.rebind(q), args...)
}

func (x *txx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return x.Tx.QueryRowContext(ctx, x.d.rebind(q), args...)
}

// insertID runs an INSERT and returns the generated id column (PostgreSQL has
// no LastInsertId, so it uses RETURNING).
func insertID(ctx context.Context, e execer, d dialect, q string, args ...any) (int64, error) {
	if d.pg() {
		var id int64
		err := e.QueryRowContext(ctx, q+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	res, err := e.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
