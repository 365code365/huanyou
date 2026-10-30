package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"vpnplatform/server/internal/backup"
	"vpnplatform/server/internal/config"
	"vpnplatform/server/internal/i18n"
	"vpnplatform/server/internal/keys"
	"vpnplatform/server/internal/provision"
	"vpnplatform/server/internal/store"
)

// System backup / restore / clone.
//
//	POST /admin/api/backup           {password, include_packages} -> encrypted .hybk download
//	POST /admin/api/backup/restore   multipart {password, file}   -> stage, restart
//	POST /admin/api/clone            {ssh…, domain, …}            -> NDJSON progress
//
// Restore never overwrites the live database while it is open: the snapshot
// is validated, staged as <db>.restore and swapped in on the next start (the
// current file is kept as .bak-pre-restore-*). The API exits after replying
// and systemd restarts it.

func (s *Server) secrets() backup.Secrets {
	sec := backup.Secrets{JWTSecret: s.cfg.JWTSecret, AdminPassword: s.cfg.Admin.Password}
	if k := s.cfg.Payments.Cashier; k != nil {
		sec.CashierSign, sec.CashierEnc = k.SignKey, k.EncKey
	}
	if e := s.cfg.Payments.Epay; e != nil {
		sec.EpayKey = e.Key
	}
	return sec
}

// writeBundle snapshots the DB and writes an encrypted bundle to w.
func (s *Server) writeBundle(ctx context.Context, w io.Writer, password string, packages bool, note string) error {
	tmp, err := os.MkdirTemp(filepath.Dir(s.cfg.Database), "backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, "vpn.db")
	if err := s.store.SnapshotTo(ctx, snap); err != nil {
		return fmt.Errorf("数据库快照失败：%w", err)
	}
	users, orders, _ := s.store.Counts(ctx)
	in := backup.Input{
		Manifest: backup.Manifest{CreatedAt: time.Now(), PublicURL: s.cfg.PublicURL, Users: users, Orders: orders,
			Nodes: len(s.cat().Nodes), Note: note},
		Secrets: s.secrets(), DBPath: snap, ReleaseDir: s.releaseDir(),
	}
	if packages {
		rels, _ := s.store.Releases(ctx, "")
		for _, r := range rels {
			if r.File != "" {
				if _, err := os.Stat(filepath.Join(s.releaseDir(), r.File)); err == nil {
					in.Packages = append(in.Packages, r.File)
				}
			}
		}
	}
	return backup.Write(w, password, in)
}

func (s *Server) adminBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password        string `json:"password"`
		IncludePackages bool   `json:"include_packages"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "weak_password", "备份密码至少 8 位（恢复时需要它，请妥善保存）")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(30 * time.Minute))
	name := "huanyou-backup-" + time.Now().Format("20060102-150405") + ".hybk"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := s.writeBundle(r.Context(), w, body.Password, body.IncludePackages, "admin download"); err != nil {
		// Headers are sent; the truncated file fails to open, which is the safe outcome.
		slog.Error("backup failed", "err", err)
		return
	}
	slog.Info("backup downloaded", "packages", body.IncludePackages)
}

// adminBackupInfo: GET /admin/api/backup/info — sizes for the UI.
func (s *Server) adminBackupInfo(w http.ResponseWriter, r *http.Request) {
	st, _ := os.Stat(s.cfg.Database)
	var dbSize, pkgSize int64
	if st != nil {
		dbSize = st.Size()
	}
	rels, _ := s.store.Releases(r.Context(), "")
	n := 0
	for _, x := range rels {
		if x.File != "" {
			pkgSize += x.Size
			n++
		}
	}
	users, orders, _ := s.store.Counts(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"db_bytes": dbSize, "package_bytes": pkgSize, "packages": n,
		"users": users, "orders": orders, "nodes": len(s.cat().Nodes), "public_url": s.cfg.PublicURL,
	})
}

func (s *Server) adminRestore(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(30 * time.Minute))
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "请使用 multipart/form-data 上传")
		return
	}
	var password string
	var opened *backup.Opened
	tmpRoot := filepath.Dir(s.cfg.Database)
	defer func() {
		if opened != nil {
			os.RemoveAll(opened.Dir)
		}
	}()
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "上传中断："+err.Error())
			return
		}
		switch part.FormName() {
		case "password":
			b, _ := io.ReadAll(io.LimitReader(part, 1<<10))
			password = string(b)
		case "file":
			if password == "" {
				writeErr(w, http.StatusBadRequest, "bad_request", "请先提交密码，再提交文件")
				return
			}
			o, err := backup.Open(part, password, tmpRoot)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "bad_backup", err.Error())
				return
			}
			opened = o
		}
	}
	if opened == nil {
		writeErr(w, http.StatusBadRequest, "no_file", "请选择备份文件")
		return
	}
	if err := store.ValidateDB(opened.DBPath); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_db", err.Error())
		return
	}
	// Packages first (harmless if the restart never happens), then stage the DB.
	_ = os.MkdirAll(s.releaseDir(), 0o755)
	for _, p := range opened.Packages {
		if releaseFile.MatchString(p) {
			_ = os.Rename(filepath.Join(opened.Dir, "releases", p), filepath.Join(s.releaseDir(), p))
		}
	}
	if err := os.Rename(opened.DBPath, store.PendingRestore(s.cfg.Database)); err != nil {
		s.internal(w, err)
		return
	}
	m := opened.Manifest
	slog.Warn("restore staged; restarting", "from", m.PublicURL, "created", m.CreatedAt, "users", m.Users)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "manifest": m,
		"message": "备份已校验，服务正在重启并恢复数据（约 5 秒）。原数据库已保留为 .bak-pre-restore 文件。",
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(800 * time.Millisecond)
		os.Exit(0) // systemd (Restart=always) brings us back; main applies the restore
	}()
}

// ---- clone ----

type cloneBody struct {
	provisionBody
	Domain        string `json:"domain"`
	Email         string `json:"email"`
	AdminPassword string `json:"admin_password"`
	Mode          string `json:"mode"` // full | config
	KeepNodes     bool   `json:"keep_nodes"`
}

// adminClone deploys a copy of this system to another server over SSH.
func (s *Server) adminClone(w http.ResponseWriter, r *http.Request) {
	var body cloneBody
	if !decode(w, r, &body) {
		return
	}
	body.Domain = strings.ToLower(strings.TrimSpace(body.Domain))
	switch {
	case strings.TrimSpace(body.Host) == "":
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写服务器 IP")
		return
	case body.Password == "" && strings.TrimSpace(body.PrivateKey) == "":
		writeErr(w, http.StatusBadRequest, "bad_request", "请填写 SSH 密码或私钥")
		return
	case !domainPattern.MatchString(body.Domain):
		writeErr(w, http.StatusBadRequest, "bad_domain", "请填写有效域名，如 vpn.example.com")
		return
	case len(body.AdminPassword) < 8:
		writeErr(w, http.StatusBadRequest, "weak_password", "新系统的后台密码至少 8 位")
		return
	case "https://"+body.Domain == s.cfg.PublicURL:
		writeErr(w, http.StatusBadRequest, "same_domain", "不能克隆到当前系统自己的域名")
		return
	}
	if body.Mode != "config" {
		body.Mode = "full"
	}
	exe, err := os.Executable()
	if err != nil {
		s.internal(w, err)
		return
	}
	apiBin, err := os.ReadFile(exe)
	if err != nil {
		s.internal(w, err)
		return
	}
	nodeBins, _ := s.loadNodeBinaries()

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(20 * time.Minute))
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	emit := func(v map[string]any) {
		_ = enc.Encode(v)
		_ = rc.Flush()
	}
	logf := func(format string, a ...any) { emit(map[string]any{"type": "log", "text": fmt.Sprintf(format, a...)}) }

	ctx, cancel := context.WithTimeout(r.Context(), 18*time.Minute)
	defer cancel()

	// 1. Database for the new system.
	logf("准备数据（%s）…\n", map[string]string{"full": "完整克隆：用户、订单、套餐、设置", "config": "仅配置：套餐、设置、客服、版本，不含用户"}[body.Mode])
	tmp, err := os.MkdirTemp(filepath.Dir(s.cfg.Database), "clone-")
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	defer os.RemoveAll(tmp)
	dbPath := filepath.Join(tmp, "vpn.db")
	if err := s.cloneDB(ctx, dbPath, body.Mode == "full", body.KeepNodes); err != nil {
		emit(map[string]any{"type": "error", "message": "准备数据失败：" + err.Error()})
		return
	}
	dbBytes, err := os.ReadFile(dbPath)
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return
	}

	// 2. Config for the new system: same static settings, new domain + secrets.
	jwt, _ := keys.RandomToken(48)
	newCfg := s.cloneConfig(body.Domain, body.Email)
	cfgJSON, _ := json.MarshalIndent(newCfg, "", "  ")
	env := fmt.Sprintf("VPN_JWT_SECRET=%s\nVPN_ADMIN_PASSWORD=%s\n", jwt, body.AdminPassword)
	sec := s.secrets()
	if sec.CashierSign != "" {
		env += "VPN_CASHIER_SIGN_KEY=" + sec.CashierSign + "\nVPN_CASHIER_ENC_KEY=" + sec.CashierEnc + "\n"
	}
	if sec.EpayKey != "" {
		env += "VPN_EPAY_KEY=" + sec.EpayKey + "\n"
	}

	res, err := provision.Clone(ctx, body.target(), provision.CloneInstall{
		Domain: body.Domain, APIBinary: apiBin, NodeBinaries: nodeBins, APIConfig: cfgJSON, APIEnv: []byte(env),
		Database: dbBytes, Log: writerFunc(func(p []byte) { emit(map[string]any{"type": "log", "text": string(p)}) }),
	})
	if err != nil {
		emit(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	emit(map[string]any{"type": "done", "url": "https://" + body.Domain, "admin": "https://" + body.Domain + "/admin/", "tls": res.TLSReady})
}

var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// cloneDB writes the database for a clone: a full snapshot, or a fresh
// database holding only the catalog (plans, settings, contacts, apps).
func (s *Server) cloneDB(ctx context.Context, path string, full, keepNodes bool) error {
	cat := s.cat().Clone()
	if !keepNodes {
		cat.Nodes = []config.Node{} // nodes report to this API; the clone installs its own
	}
	if full {
		if err := s.store.SnapshotTo(ctx, path); err != nil {
			return err
		}
	}
	st, err := store.Open(path) // fresh DB gets the full schema here
	if err != nil {
		return err
	}
	defer st.Close()
	if err := cat.Normalize(); err != nil {
		return err
	}
	if !full {
		// Language packs and the AI translator setting are configuration too.
		packs, err := s.store.LangPacks(ctx, false)
		if err != nil {
			return err
		}
		for _, p := range packs {
			pk, err := s.store.LangPack(ctx, p.Code)
			if err != nil {
				return err
			}
			if _, err := st.SaveLangPack(ctx, pk); err != nil {
				return err
			}
		}
		var ai i18n.AIConfig
		if ok, _ := s.store.Setting(ctx, aiSettingKey, &ai); ok {
			if err := st.PutSetting(ctx, aiSettingKey, ai); err != nil {
				return err
			}
		}
	}
	return st.SaveCatalog(ctx, cat)
}

// cloneConfig is this system's api.json adapted for the new domain. Secrets
// go to api.env instead; the catalog seed is irrelevant (the DB has it).
func (s *Server) cloneConfig(domain, email string) map[string]any {
	c := map[string]any{
		"listen":             "127.0.0.1:8080",
		"database":           "/var/lib/vpn/vpn.db",
		"token_ttl_hours":    s.cfg.TokenTTLHours,
		"allow_register":     s.cfg.AllowRegister,
		"trust_proxy_header": false,
		"public_url":         "https://" + domain,
		"download_dir":       "/var/lib/vpn/dl",
		"admin":              map[string]any{},
		"tls":                map[string]any{"domain": domain, "email": email},
		"provision": map[string]any{
			"binary_amd64": "/usr/local/bin/vpn-node", "reality_target": s.cfg.Provision.RealityTarget,
		},
	}
	pay := map[string]any{"mock": s.cfg.Payments.Mock}
	if k := s.cfg.Payments.Cashier; k != nil {
		pay["cashier"] = map[string]any{"api_url": k.APIURL, "merchant_id": k.MerchantID, "channels": k.Channels}
	}
	if e := s.cfg.Payments.Epay; e != nil {
		pay["epay"] = map[string]any{"api_url": e.APIURL, "pid": e.PID, "types": e.Types}
	}
	c["payments"] = pay
	return c
}
