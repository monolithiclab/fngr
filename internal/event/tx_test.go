package event

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// TestInTx covers the transaction bracket every mutation in this package now
// goes through. The commit path is exercised by most of the package's tests
// already; what needs naming directly is the wrapping, since the reason inTx
// exists is that two hand-written brackets returned tx.Commit() bare.
func TestInTx(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")

	t.Run("commits and returns the value", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)

		got, err := inTx(ctx, database, func(tx *sql.Tx) (int64, error) {
			res, err := tx.ExecContext(ctx, "INSERT INTO events (title) VALUES ('kept')")
			if err != nil {
				return 0, err
			}
			return res.LastInsertId()
		})
		if err != nil {
			t.Fatalf("inTx: %v", err)
		}
		if got == 0 {
			t.Error("inTx returned id 0, want the inserted row's id")
		}
		if n := countEvents(t, database); n != 1 {
			t.Errorf("events = %d, want 1", n)
		}
	})

	t.Run("rolls back and passes the error through unwrapped", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)

		got, err := inTx(ctx, database, func(tx *sql.Tx) (int64, error) {
			if _, err := tx.ExecContext(ctx, "INSERT INTO events (title) VALUES ('dropped')"); err != nil {
				return 0, err
			}
			return 7, sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("inTx err = %v, want %v", err, sentinel)
		}
		// The zero value, not the 7 fn returned alongside its error — a
		// caller that ignores err must not see a half-written result.
		if got != 0 {
			t.Errorf("inTx value = %d, want 0 on error", got)
		}
		if n := countEvents(t, database); n != 0 {
			t.Errorf("events = %d, want 0 after rollback", n)
		}
	})

	t.Run("names the begin", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)
		if err := database.Close(); err != nil {
			t.Fatalf("close: %v", err)
		}

		_, err := inTx(ctx, database, func(*sql.Tx) (int64, error) {
			t.Error("fn ran against a transaction that could not be opened")
			return 0, nil
		})
		if err == nil || !strings.Contains(err.Error(), "begin transaction:") {
			t.Fatalf("inTx err = %v, want one naming the begin", err)
		}
	})

	t.Run("names the commit", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)

		// Committing inside fn leaves inTx's own Commit to fail with
		// sql.ErrTxDone, which is the only way to reach that branch without
		// a fault injector.
		_, err := inTx(ctx, database, func(tx *sql.Tx) (int64, error) {
			return 0, tx.Commit()
		})
		if err == nil || !strings.Contains(err.Error(), "commit transaction:") {
			t.Fatalf("inTx err = %v, want one naming the commit", err)
		}
	})
}

// TestInTxVoid covers the no-result wrapper. Both paths, because the struct{}
// adapter is easy to get backwards.
func TestInTxVoid(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")

	t.Run("commits", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)

		err := inTxVoid(ctx, database, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO events (title) VALUES ('kept')")
			return err
		})
		if err != nil {
			t.Fatalf("inTxVoid: %v", err)
		}
		if n := countEvents(t, database); n != 1 {
			t.Errorf("events = %d, want 1", n)
		}
	})

	t.Run("rolls back", func(t *testing.T) {
		t.Parallel()
		database := testDB(t)

		err := inTxVoid(ctx, database, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO events (title) VALUES ('dropped')"); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("inTxVoid err = %v, want %v", err, sentinel)
		}
		if n := countEvents(t, database); n != 0 {
			t.Errorf("events = %d, want 0 after rollback", n)
		}
	})
}

func countEvents(t *testing.T, database *sql.DB) int {
	t.Helper()
	var n int
	if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}
