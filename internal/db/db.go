package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func ResolvePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if _, err := os.Stat(".fngr.db"); err == nil {
		return ".fngr.db", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".fngr.db"), nil
}

// dsn builds the driver connection string. The pragmas ride in the DSN
// rather than being issued as `db.Exec("PRAGMA ...")` after opening, and that
// is load-bearing: *sql.DB is a pool, so an Exec configures whichever
// connection happens to be free and leaves every other one the pool opens
// later at SQLite's defaults — foreign_keys=OFF and busy_timeout=0.
// Concurrent writers then fail instantly with SQLITE_BUSY and their events
// are silently lost. The driver replays `_pragma=` parameters on every new
// connection it establishes. Don't move them back out.
//
// The order below is cosmetic — the driver's applyQueryParams sorts
// busy_timeout to the front itself, which is what stops journal_mode(WAL)
// racing its own brief exclusive lock. Re-check that on driver upgrades.
//
// URI escaping matters because the driver leaves the query string in the
// filename and opens with SQLITE_OPEN_URI, so SQLite parses the path itself
// and '?', '#' and '%' are all special. OmitHost keeps a relative path out
// of the authority position (`file:.fngr.db`, not `file://.fngr.db`).
func dsn(path string) string {
	u := url.URL{
		Scheme:   "file",
		Path:     path,
		OmitHost: true,
		RawQuery: "_pragma=busy_timeout(5000)" +
			"&_pragma=foreign_keys(ON)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=synchronous(NORMAL)",
	}
	return u.String()
}

func Open(path string, create bool) (*sql.DB, error) {
	if !create {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("database not found: %s (use 'fngr add' to create one)", path)
		}
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}

	// sql.Open never contacts the database, so an unusable path would
	// otherwise first surface from an arbitrary later query. Ping forces one
	// connection — and with it the pragmas above — to be established here.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot open database %s: %w", path, err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot migrate schema: %w", err)
	}

	return db, nil
}
