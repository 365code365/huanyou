package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"vpnplatform/server/internal/config"
)

const catalogKey = "catalog"

// LoadCatalog returns the stored catalog, or (nil, nil) if none is saved yet.
func (s *Store) LoadCatalog(ctx context.Context) (*config.Catalog, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE skey = ?`, catalogKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c config.Catalog
	if err := json.Unmarshal([]byte(v), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveCatalog stores the catalog (the caller validates it first).
func (s *Store) SaveCatalog(ctx context.Context, c *config.Catalog) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.putSettingRaw(ctx, catalogKey, string(b))
}
