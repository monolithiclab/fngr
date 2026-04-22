package db

import (
	"database/sql"
	"embed"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// migration is a single forward step from version N-1 to N.
type migration struct {
	version int
	up      io.Reader
}

// dbExec accepts either *sql.DB or *sql.Tx.
type dbExec interface {
	Exec(string, ...any) (sql.Result, error)
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

// loadMigrations reads every embedded `<N>.sql` file and returns them sorted
// by version. Never edit a published migration; add new versions by dropping
// a new `<N>.sql` file into `internal/db/migrations/`.
func loadMigrations() []migration {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		panic(fmt.Sprintf("read embedded migrations: %v", err))
	}

	out := make([]migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".sql")
		v, err := strconv.Atoi(name)
		if err != nil {
			panic(fmt.Sprintf("migration filename %q must be <version>.sql: %v", e.Name(), err))
		}
		f, err := migrationsFS.Open(path.Join("migrations", e.Name()))
		if err != nil {
			panic(fmt.Sprintf("read migration %s: %v", e.Name(), err))
		}
		out = append(out, migration{version: v, up: f})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			panic(fmt.Sprintf("migrations must be contiguous starting at 1; got version %d at index %d", m.version, i))
		}
	}
	return out
}

// migrate applies any pending migrations. Pre-migration databases (created
// before this code shipped) carry the v1 schema with user_version still at 0;
// detect that case and bump the version so we don't try to recreate tables
// that already exist.
func migrate(db *sql.DB) error {
	current, err := userVersion(db)
	if err != nil {
		return err
	}

	if current == 0 {
		legacy, err := hasLegacyV1Schema(db)
		if err != nil {
			return err
		}
		if legacy {
			if err := setUserVersion(db, 1); err != nil {
				return err
			}
			current = 1
		}
	}

	for _, m := range loadMigrations() {
		if m.version <= current {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
	}
	return nil
}

func userVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return v, nil
}

func setUserVersion(db dbExec, v int) error {
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil { // #nosec G201 -- v is an internal int constant
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

func hasLegacyV1Schema(db *sql.DB) (bool, error) {
	var n int
	err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'events'",
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("probe legacy schema: %w", err)
	}
	return n == 1, nil
}

// applyMigration runs the migration body and bumps user_version inside a
// single transaction; failure on either step rolls back both. Migration
// SQL should still prefer `CREATE ... IF NOT EXISTS` and `DROP ... IF
// EXISTS` clauses so manual recovery (e.g. after a crash mid-Exec) finds
// a re-runnable script even if the planner ever surfaces a path that
// commits before tx.Commit returns.
func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	sql, err := io.ReadAll(m.up)
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := tx.Exec(string(sql)); err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if err := setUserVersion(tx, m.version); err != nil {
		return fmt.Errorf("bump user_version: %w", err)
	}
	return tx.Commit()
}
