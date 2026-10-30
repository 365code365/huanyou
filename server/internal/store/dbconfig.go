package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DBConfig selects the primary database. It lives in database.json next to
// the SQLite file (written by the admin panel, mode 0600 because the DSN holds
// a password). Without it the SQLite file is the primary database.
type DBConfig struct {
	Driver string `json:"driver"` // sqlite | mysql | postgres
	DSN    string `json:"dsn"`    // ignored for sqlite
	// CatchUp is the database used before the last switch. On the next start
	// rows written to it after the final sync are copied over once, then the
	// field is cleared.
	CatchUp *DBConfig `json:"catch_up,omitempty"`
	// CatchUpSince is the start time of the final sync before the switch.
	CatchUpSince int64 `json:"catch_up_since,omitempty"`
}

// DBConfigPath returns the config file location for a SQLite database path.
func DBConfigPath(sqlitePath string) string {
	return filepath.Join(filepath.Dir(sqlitePath), "database.json")
}

// LoadDBConfig reads the config; (nil, nil) if there is none. The environment
// variables VPN_DB_DRIVER / VPN_DB_DSN take precedence (containers).
func LoadDBConfig(path string) (*DBConfig, error) {
	if d := os.Getenv("VPN_DB_DRIVER"); d != "" {
		return &DBConfig{Driver: d, DSN: os.Getenv("VPN_DB_DSN")}, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c DBConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func SaveDBConfig(path string, c *DBConfig) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// OpenConfigured opens the database described by c, or the SQLite file when
// c is nil / sqlite.
func OpenConfigured(c *DBConfig, sqlitePath string) (*Store, error) {
	if c == nil || c.Driver == "" || c.Driver == DriverSQLite {
		return Open(sqlitePath)
	}
	return OpenDSN(c.Driver, c.DSN)
}

// ValidateDSN checks the shape of a DSN before connecting.
func ValidateDSN(driver, dsn string) error {
	dsn = strings.TrimSpace(dsn)
	switch driver {
	case DriverSQLite:
		return nil
	case DriverMySQL:
		if !mysqlDSNRe.MatchString(dsn) {
			return errors.New("MySQL 连接串格式：用户名:密码@tcp(主机:3306)/数据库名")
		}
	case DriverPostgres:
		u, err := url.Parse(dsn)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || strings.Trim(u.Path, "/") == "" {
			return errors.New("PostgreSQL 连接串格式：postgres://用户名:密码@主机:5432/数据库名?sslmode=disable")
		}
	default:
		return fmt.Errorf("不支持的数据库类型 %q", driver)
	}
	return nil
}

var mysqlDSNRe = regexp.MustCompile(`^[^:@/]+(:[^@]*)?@tcp\([^)]+\)/[A-Za-z0-9_$-]+(\?.*)?$`)

// MaskDSN hides the password in a DSN for display.
func MaskDSN(driver, dsn string) string {
	switch driver {
	case DriverMySQL:
		if i, j := strings.Index(dsn, ":"), strings.LastIndex(dsn, "@tcp("); i >= 0 && j > i {
			return dsn[:i+1] + "******" + dsn[j:]
		}
	case DriverPostgres:
		if u, err := url.Parse(dsn); err == nil && u.User != nil {
			if _, ok := u.User.Password(); ok {
				u.User = url.UserPassword(u.User.Username(), "xxxxxx")
				return strings.Replace(u.String(), ":xxxxxx@", ":******@", 1)
			}
			return u.String()
		}
	}
	return dsn
}

// Target identifies a database for sync watermarks (driver + DSN without password).
func Target(driver, dsn string) string { return driver + " " + MaskDSN(driver, dsn) }
