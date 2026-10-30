package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrBuildExists = errors.New("build already exists")

// Release is an uploaded (or linked) app version.
type Release struct {
	ID       int64  `json:"id"`
	Platform string `json:"platform"` // android | ios
	Version  string `json:"version"`
	Build    int64  `json:"build"`
	Notes    string `json:"notes"`
	File     string `json:"file"` // stored package under download_dir/releases ("" = external URL)
	URL      string `json:"url"`  // external link
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	BundleID string `json:"bundle_id"`
	Force    bool   `json:"force"` // older builds must update
	// ABI of an Android APK built with --split-per-abi ("arm64-v8a",
	// "armeabi-v7a", "x86_64"); "" = universal / any device.
	ABI       string    `json:"abi"`
	Published bool      `json:"published"`
	CreatedAt time.Time `json:"created_at"`
}

const releaseCols = `id, platform, version, build, notes, file, url, size, sha256, bundle_id, force_update, published, abi, created_at`

func scanRelease(row interface{ Scan(...any) error }) (*Release, error) {
	var r Release
	var force, pub int
	var created int64
	if err := row.Scan(&r.ID, &r.Platform, &r.Version, &r.Build, &r.Notes, &r.File, &r.URL, &r.Size,
		&r.SHA256, &r.BundleID, &force, &pub, &r.ABI, &created); err != nil {
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
	now := time.Now().Unix()
	id, err := insertID(ctx, s.db, s.d, `INSERT INTO releases (platform, version, build, notes, file, url, size, sha256, bundle_id, force_update, published, abi, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Platform, r.Version, r.Build, r.Notes, r.File, r.URL, r.Size, r.SHA256, r.BundleID,
		b2i(r.Force), b2i(r.Published), r.ABI, now, now)
	if err != nil {
		if isUnique(err) {
			return nil, ErrBuildExists
		}
		return nil, err
	}
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

// LatestRelease returns the newest published release a device with this ABI
// can install: an APK for exactly its ABI, or a universal one. With abi ""
// (download site, older apps that don't report it) the 64-bit ARM build is
// preferred, as nearly all current phones run it.
func (s *Store) LatestRelease(ctx context.Context, platform, abi string) (*Release, error) {
	q := `SELECT ` + releaseCols + ` FROM releases WHERE platform = ? AND published = 1`
	args := []any{platform}
	if abi != "" {
		q += ` AND (abi = '' OR abi = ?)`
		args = append(args, abi)
	}
	q += ` ORDER BY build DESC, CASE WHEN abi = ? THEN 0 WHEN abi = 'arm64-v8a' THEN 1 WHEN abi = '' THEN 2 ELSE 3 END LIMIT 1`
	args = append(args, abi)
	return scanRelease(s.db.QueryRowContext(ctx, q, args...))
}

// LatestBuilds returns the newest published release per ABI of the platform's
// latest build (the download site lists them all).
func (s *Store) LatestBuilds(ctx context.Context, platform string) ([]*Release, error) {
	all, err := s.Releases(ctx, platform)
	if err != nil {
		return nil, err
	}
	var out []*Release
	top := int64(-1)
	for _, r := range all { // newest build first
		if !r.Published {
			continue
		}
		if top < 0 {
			top = r.Build
		}
		if r.Build != top {
			break
		}
		out = append(out, r)
	}
	return out, nil
}

// ForcedSince reports whether any published release newer than build is marked
// force (a forced update can't be skipped by jumping past it).
func (s *Store) ForcedSince(ctx context.Context, platform string, build int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM releases WHERE platform = ? AND published = 1 AND force_update = 1 AND build > ?`,
		platform, build).Scan(&n)
	return n > 0, err
}

func (s *Store) UpdateRelease(ctx context.Context, id int64, notes string, force, published bool, abi string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE releases SET notes = ?, force_update = ?, published = ?, abi = ?, updated_at = ? WHERE id = ?`,
		notes, b2i(force), b2i(published), abi, time.Now().Unix(), id)
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
