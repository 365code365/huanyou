package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"vpnplatform/server/internal/store"
)

// Database settings: test a MySQL / PostgreSQL connection, copy all data to
// it (full or incremental, repeatable), and switch the primary database.
//
// The switch does a final full sync, writes database.json and restarts the
// API (systemd brings it back). On start the API copies anything written to
// the old database after that final sync, then serves from the new one.

var dbOpMu sync.Mutex // one sync / switch at a time

func (s *Server) registerAdminDB(a func(string, http.HandlerFunc)) {
	a("GET /admin/api/database", s.adminDBInfo)
	a("POST /admin/api/database/test", s.adminDBTest)
	a("POST /admin/api/database/sync", s.adminDBSync)
	a("POST /admin/api/database/switch", s.adminDBSwitch)
}

type dbTarget struct {
	Driver string `json:"driver"`
	DSN    string `json:"dsn"`
}

func (s *Server) dbConfigPath() string { return store.DBConfigPath(s.cfg.Database) }

func (s *Server) adminDBInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, _ := store.LoadDBConfig(s.dbConfigPath())
	driver, dsn := store.DriverSQLite, s.cfg.Database
	if cur != nil && cur.Driver != "" && cur.Driver != store.DriverSQLite {
		driver, dsn = cur.Driver, store.MaskDSN(cur.Driver, cur.DSN)
	}
	counts, err := s.store.TableCounts(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	marks, _ := s.store.SyncMarks(ctx)
	start := time.Now()
	pingErr := s.store.Ping(ctx)
	info := map[string]any{
		"driver": driver, "dsn": dsn, "schema_version": store.SchemaVersion, "counts": counts,
		"ping_ms": time.Since(start).Milliseconds(), "marks": marks, "sqlite_path": s.cfg.Database,
		"env_override": os.Getenv("VPN_DB_DRIVER") != "",
	}
	if pingErr != nil {
		info["ping_error"] = pingErr.Error()
	}
	writeJSON(w, http.StatusOK, info)
}

// openTarget validates and opens a target (creating its schema), refusing the
// database currently in use.
func (s *Server) openTarget(t dbTarget) (*store.Store, error) {
	t.Driver, t.DSN = strings.TrimSpace(t.Driver), strings.TrimSpace(t.DSN)
	if t.Driver == store.DriverSQLite {
		if s.store.Driver() == store.DriverSQLite {
			return nil, errors.New("当前已经在使用 SQLite")
		}
		return store.Open(s.cfg.Database)
	}
	if err := store.ValidateDSN(t.Driver, t.DSN); err != nil {
		return nil, err
	}
	if cur, _ := store.LoadDBConfig(s.dbConfigPath()); cur != nil && cur.Driver == t.Driver && cur.DSN == t.DSN {
		return nil, errors.New("目标就是当前正在使用的数据库")
	}
	return store.OpenDSN(t.Driver, t.DSN)
}

func (s *Server) adminDBTest(w http.ResponseWriter, r *http.Request) {
	var t dbTarget
	if !decode(w, r, &t) {
		return
	}
	start := time.Now()
	dst, err := s.openTarget(t)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "db_failed", err.Error())
		return
	}
	defer dst.Close()
	counts, err := dst.TableCounts(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadRequest, "db_failed", err.Error())
		return
	}
	empty := true
	for _, n := range counts {
		if n > 0 {
			empty = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "ms": time.Since(start).Milliseconds(), "counts": counts, "empty": empty,
		"message": "连接成功，表结构已就绪",
	})
}

// adminDBSync streams NDJSON progress: {"type":"progress","table","rows"} …
// then {"type":"done","result"} or {"type":"error","message"}.
func (s *Server) adminDBSync(w http.ResponseWriter, r *http.Request) {
	var body struct {
		dbTarget
		Full bool `json:"full"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !dbOpMu.TryLock() {
		writeErr(w, http.StatusConflict, "busy", "已有同步任务在进行")
		return
	}
	defer dbOpMu.Unlock()
	dst, err := s.openTarget(body.dbTarget)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "db_failed", err.Error())
		return
	}
	defer dst.Close()

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(2 * time.Hour))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	enc := json.NewEncoder(w)
	emit := func(v any) {
		_ = enc.Encode(v)
		_ = rc.Flush()
	}
	target := store.Target(body.Driver, body.DSN)
	marks, _ := s.store.SyncMarks(r.Context())
	prev, had := marks[target]
	opts := store.SyncOptions{Full: body.Full || !had}
	if !opts.Full {
		opts.Since = prev.At - 120 // overlap: rows written while the last run was reading
	}
	last := time.Time{}
	opts.Progress = func(table string, n int) {
		if time.Since(last) > 300*time.Millisecond {
			last = time.Now()
			emit(map[string]any{"type": "progress", "table": table, "rows": n})
		}
	}
	emit(map[string]any{"type": "start", "full": opts.Full, "since": opts.Since})
	// Detach from the request so a closed browser tab doesn't stop a half-done copy.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	res, err := store.Sync(ctx, s.store, dst, opts)
	if err != nil {
		slog.Error("database sync failed", "target", target, "err", err)
		emit(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	_ = s.store.PutSyncMark(ctx, target, store.SyncMark{At: res.StartedAt, Full: opts.Full, Seconds: res.Seconds, Result: res.Tables})
	slog.Info("database synced", "target", target, "full", opts.Full, "seconds", res.Seconds)
	emit(map[string]any{"type": "done", "result": res})
}

func (s *Server) adminDBSwitch(w http.ResponseWriter, r *http.Request) {
	var t dbTarget
	if !decode(w, r, &t) {
		return
	}
	if os.Getenv("VPN_DB_DRIVER") != "" {
		writeErr(w, http.StatusConflict, "env_override", "数据库由环境变量 VPN_DB_DRIVER 指定，请修改环境变量后重启")
		return
	}
	if !dbOpMu.TryLock() {
		writeErr(w, http.StatusConflict, "busy", "已有同步任务在进行")
		return
	}
	defer dbOpMu.Unlock()
	dst, err := s.openTarget(t)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "db_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	s.flushIPs(ctx)
	res, err := store.Sync(ctx, s.store, dst, store.SyncOptions{Full: true})
	dst.Close()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "sync_failed", "最终同步失败，未切换："+err.Error())
		return
	}
	cur, _ := store.LoadDBConfig(s.dbConfigPath())
	if cur == nil {
		cur = &store.DBConfig{Driver: store.DriverSQLite}
	}
	cur.CatchUp = nil
	next := &store.DBConfig{Driver: t.Driver, DSN: strings.TrimSpace(t.DSN), CatchUp: cur, CatchUpSince: res.StartedAt}
	if t.Driver == store.DriverSQLite {
		next.DSN = ""
	}
	if err := store.SaveDBConfig(s.dbConfigPath(), next); err != nil {
		s.internal(w, err)
		return
	}
	slog.Warn("switching primary database; restarting", "to", store.Target(t.Driver, t.DSN), "seconds", res.Seconds)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "result": res,
		"message": "数据已同步，服务正在重启并切换到新数据库（约 5 秒）。原数据库保留不动，可随时切回。",
	})
	_ = http.NewResponseController(w).Flush()
	go func() {
		time.Sleep(800 * time.Millisecond)
		os.Exit(0) // systemd restarts the API, which opens the new database
	}()
}
