package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
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

func Open(path string, create bool) (*sql.DB, error) {
	if !create {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("database not found: %s (use 'fngr add' to create one)", path)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}

	pragmas := []struct{ key, value string }{
		{"foreign_keys", "ON"},
		{"journal_mode", "WAL"},
		{"busy_timeout", "5000"},
		{"synchronous", "NORMAL"},
	}

	for _, p := range pragmas {
		query := fmt.Sprintf("PRAGMA %s = %s", p.key, p.value)
		if _, err := db.Exec(query); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("cannot set pragma %s: %w", p.key, err)
		}
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot migrate schema: %w", err)
	}

	return db, nil
}
