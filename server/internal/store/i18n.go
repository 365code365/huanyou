package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// LangPack is one downloadable app language.
type LangPack struct {
	Code       string            `json:"code"`
	Name       string            `json:"name"`        // Chinese name, for the admin
	NativeName string            `json:"native_name"` // shown in the app
	RTL        bool              `json:"rtl"`
	Enabled    bool              `json:"enabled"`
	Version    int64             `json:"version"` // bumps on every save; the app re-downloads when it changes
	Strings    map[string]string `json:"strings,omitempty"`
	Count      int               `json:"count"` // non-empty translations
	UpdatedAt  time.Time         `json:"updated_at"`
}

func scanLangPack(row interface{ Scan(...any) error }, withStrings bool) (*LangPack, error) {
	var p LangPack
	var rtl, en int
	var updated int64
	var raw string
	if err := row.Scan(&p.Code, &p.Name, &p.NativeName, &rtl, &en, &p.Version, &raw, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.RTL, p.Enabled, p.UpdatedAt = rtl != 0, en != 0, time.Unix(updated, 0)
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	for _, v := range m {
		if v != "" {
			p.Count++
		}
	}
	if withStrings {
		p.Strings = m
	}
	return &p, nil
}

const langCols = `code, name, native_name, rtl, enabled, version, strings, updated_at`

// LangPacks lists packs without their strings.
func (s *Store) LangPacks(ctx context.Context, enabledOnly bool) ([]*LangPack, error) {
	q := `SELECT ` + langCols + ` FROM lang_packs`
	if enabledOnly {
		q += ` WHERE enabled = 1`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY sort, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LangPack
	for rows.Next() {
		p, err := scanLangPack(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) LangPack(ctx context.Context, code string) (*LangPack, error) {
	return scanLangPack(s.db.QueryRowContext(ctx, `SELECT `+langCols+` FROM lang_packs WHERE code = ?`, code), true)
}

// SaveLangPack inserts or replaces a pack and bumps its version.
func (s *Store) SaveLangPack(ctx context.Context, p *LangPack) (*LangPack, error) {
	if p.Strings == nil {
		p.Strings = map[string]string{}
	}
	b, err := json.Marshal(p.Strings)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var sort int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort), 0) + 1 FROM lang_packs`).Scan(&sort); err != nil {
		return nil, err
	}
	// Version must strictly increase even for two saves in one second.
	q := s.d.upsert("lang_packs",
		[]string{"code", "name", "native_name", "rtl", "enabled", "version", "strings", "updated_at", "sort"}, []string{"code"},
		"name = NEW(name)", "native_name = NEW(native_name)", "rtl = NEW(rtl)", "enabled = NEW(enabled)",
		"strings = NEW(strings)", "updated_at = NEW(updated_at)",
		"version = "+s.d.greatest("lang_packs.version + 1", "NEW(version)"))
	_, err = s.db.ExecContext(ctx, q,
		p.Code, p.Name, p.NativeName, b2i(p.RTL), b2i(p.Enabled), now.Unix(), string(b), now.Unix(), sort)
	if err != nil {
		return nil, err
	}
	return s.LangPack(ctx, p.Code)
}

func (s *Store) DeleteLangPack(ctx context.Context, code string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM lang_packs WHERE code = ?`, code)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Setting reads a JSON value from the settings table into v; found=false if absent.
func (s *Store) Setting(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE skey = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

func (s *Store) PutSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.putSettingRaw(ctx, key, string(b))
}

func (s *Store) putSettingRaw(ctx context.Context, key, value string) error {
	q := s.d.upsert("settings", []string{"skey", "value", "updated_at"}, []string{"skey"},
		"value = NEW(value)", "updated_at = NEW(updated_at)")
	_, err := s.db.ExecContext(ctx, q, key, value, time.Now().Unix())
	return err
}
