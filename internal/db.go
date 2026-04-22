package internal

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

func ResolveDBPath(explicit string) (string, error) {
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

func OpenDB(path string, create bool) (*sql.DB, error) {
	if !create {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil, fmt.Errorf("database not found: %s (use 'fngr add' to create one)", path)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cannot open database: %w", err)
	}

	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot enable foreign keys: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot enable WAL mode: %w", err)
	}

	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot initialize schema: %w", err)
	}

	return db, nil
}

func initSchema(db *sql.DB) error {
	return nil
}
