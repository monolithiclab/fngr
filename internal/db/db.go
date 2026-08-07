package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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

// openHint translates the driver errors that name the database whatever is
// actually at fault, and returns "" for every other one so it keeps its own
// text. SQLite answers SQLITE_CANTOPEN — "unable to open database file" — for
// a path that is a directory, one whose parent is missing, and one whose
// parent is not writable alike; once the file does exist the same three
// become SQLITE_READONLY, so a plain `fngr list` reports "attempt to write a
// readonly database" about a directory it never meant to write. Everything
// needed to tell them apart is on the filesystem, so read it there.
//
// The pairing is with the driver's error *type* and result codes, not with
// its message strings, so a driver bump can reword freely; check this on one
// that renumbers, which SQLite's ABI does not permit.
func openHint(path string, err error) string {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return ""
	}
	// Extended result codes carry the primary one in the low byte —
	// SQLITE_READONLY_DIRECTORY is 1544, not 8.
	switch serr.Code() & 0xff {
	case sqlite3.SQLITE_READONLY, sqlite3.SQLITE_CANTOPEN:
		return pathHint(path)
	}
	return ""
}

// pathHint names the filesystem object standing in the way of `path`, or ""
// when nothing there explains the failure and the driver's own text is the
// better answer.
func pathHint(path string) string {
	switch fi, err := os.Stat(path); {
	case err != nil:
		// Not there, or not visible from here — either way the directory
		// that would hold it is the thing to look at.
	case fi.IsDir():
		return "is a directory, not a database file"
	default:
		// Exists, but SQLite could not open it. A file the user cannot read
		// is the usual reason — one `sudo fngr add` leaves a root-owned
		// database behind — and no amount of looking at the directory finds
		// it. Any other failure to open falls through: it says nothing the
		// directory checks below might not say better.
		// #nosec G304 -- path is the database the caller already asked us to
		// open; this only asks whether that same open would have been
		// permitted, and reads nothing.
		if f, err := os.Open(path); err == nil {
			_ = f.Close()
		} else if errors.Is(err, fs.ErrPermission) {
			return "is not readable"
		}
	}
	return dirHint(filepath.Dir(path), filepath.Base(path))
}

// dirHint asks whether `dir` could hold a database called `base`. WAL is what
// makes the directory rather than the file load-bearing: SQLite has to create
// the `-wal` and `-shm` siblings beside the database, so a directory the user
// cannot write fails a plain `fngr list` exactly as it fails an `fngr add`.
//
// The write probe is a real create-and-unlink, not a read of the mode bits,
// because ACLs, read-only mounts and a container UID that does not own the
// volume all deny a write the bits allow. It runs on the "no database yet"
// path too, where nothing has gone wrong yet — an accepted cost, since that
// is precisely the run where a user who cannot write the directory needs
// telling, and the alternative is `fngr add` failing for reasons `fngr list`
// declined to look for.
func dirHint(dir, base string) string {
	// Absolute throughout: ResolvePath's cwd branch yields ".", and
	// `directory . is not writable` names nothing the user can go and fix.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	switch fi, err := os.Stat(dir); {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("directory %s does not exist", dir)
	case err != nil:
		return ""
	case !fi.IsDir():
		return fmt.Sprintf("%s is not a directory", dir)
	}
	// Only a denial is reported. A full disk or a process out of file
	// descriptors fails the probe too, and the latter is itself a reason
	// SQLite returns SQLITE_CANTOPEN; since the hint replaces the driver's
	// text rather than joining it, guessing there would substitute a false
	// diagnosis for a true one.
	if probe, err := os.CreateTemp(dir, ".fngr-probe-*"); err == nil {
		_ = probe.Close()
		_ = os.Remove(probe.Name())
	} else if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EROFS) {
		return fmt.Sprintf("directory %s is not writable — SQLite creates %s-wal and %s-shm "+
			"beside the database, so even reading it needs one", dir, base, base)
	}
	return ""
}

func Open(path string, create bool) (*sql.DB, error) {
	if !create {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			// A missing file is the ordinary case — but only when the
			// directory around it could hold one, otherwise 'fngr add'
			// is advice that fails the same way. Straight to dirHint:
			// the stat above already settled the file half.
			if hint := dirHint(filepath.Dir(path), filepath.Base(path)); hint != "" {
				return nil, fmt.Errorf("cannot open database %s: %s", path, hint)
			}
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
		if hint := openHint(path, err); hint != "" {
			// The hint replaces the driver text rather than joining it:
			// "unable to open database file (14)" is what we are here to
			// translate, and repeating it buries the translation.
			return nil, fmt.Errorf("cannot open database %s: %s", path, hint)
		}
		return nil, fmt.Errorf("cannot open database %s: %w", path, err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot migrate schema: %w", err)
	}

	return db, nil
}
