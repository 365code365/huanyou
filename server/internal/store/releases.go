package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var ErrBuildExists = errors.New("build already exists")

// Release is an uploaded (or linked) app version.
type Release struct {
	ID        int64     `json:"id"`
	Platform  string    `json:"platform"` // android | ios
	Version   string    `json:"version"`
	Build     int64     `json:"build"`
	Notes     string    `json:"notes"`
	File      string    `json:"file"` // stored package under download_dir/releases ("" = external URL)
	URL       string    `json:"url"`  // external link
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	BundleID  string    `json:"bundle_id"`
	Force     bool      `json:"force"` // older builds must update
	Published bool      `json:"published"`
	CreatedAt time.Time `json:"created_at"`
}

const releaseCols = `id, platform, version, build, notes, file, url, size, sha256, bundle_id, force, published, created_at`

func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var r Release
	var force, pub int
	var created int64
	if err := row.Scan(&r.ID, &r.Platform, &r.Version, &r.Build, &r.Notes, &r.File, &r.URL, &r.Size,
		&r.SHA256, &r.BundleID, &force, &pub, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Force, r.Published, r.CreatedAt = force != 0, pub != 0, time.Unix(created, 0)
	return &r, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) CreateRelease(ctx context.Context, r *Release) (*Release, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO releases (platform, version, build, notes, file, url, size, sha256, bundle_id, force, published, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Platform, r.Version, r.Build, r.Notes, r.File, r.URL, r.Size, r.SHA256, r.BundleID,
		b2i(r.Force), b2i(r.Published), time.Now().Unix())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrBuildExists
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.ReleaseByID(ctx, id)
}

func (s *Store) ReleaseByID(ctx context.Context, id int64) (*Release, error) {
	return scanRelease(s.db.QueryRowContext(ctx, `SELECT `+releaseCols+` FROM releases WHERE id = ?`, id))
}

// Releases lists releases newest build first; platform "" = all.
func (s *Store) Releases(ctx context.Context, platform string) ([]*Release, error) {
	q := `SELECT ` + releaseCols + ` FROM releases`
	var args []any
	if platform != "" {
		q += ` WHERE platform = ?`
		args = append(args, platform)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY platform, build DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Release{}
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestRelease is the newest published release for a platform.
func (s *Store) LatestRelease(ctx context.Context, platform string) (*Release, error) {
	return scanRelease(s.db.QueryRowContext(ctx,
		`SELECT `+releaseCols+` FROM releases WHERE platform = ? AND published = 1 ORDER BY build DESC LIMIT 1`, platform))
}

// ForcedSince reports whether any published release newer than build is marked
// force (a forced update can't be skipped by jumping past it).
func (s *Store) ForcedSince(ctx context.Context, platform string, build int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM releases WHERE platform = ? AND published = 1 AND force = 1 AND build > ?`,
		platform, build).Scan(&n)
	return n > 0, err
}

func (s *Store) UpdateRelease(ctx context.Context, id int64, notes string, force, published bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE releases SET notes = ?, force = ?, published = ? WHERE id = ?`,
		notes, b2i(force), b2i(published), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteRelease(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
