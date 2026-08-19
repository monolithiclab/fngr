package event

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
)

func TestAdd(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	meta := []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "meeting"},
		{Key: "people", Value: "bob"},
	}

	id, err := Add(ctx, database, AddInput{Title: "standup with @bob #meeting", Meta: meta})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id < 1 {
		t.Fatalf("expected positive event ID, got %d", id)
	}

	var text string
	err = database.QueryRow("SELECT title FROM events WHERE id = ?", id).Scan(&text)
	if err != nil {
		t.Fatalf("query event row: %v", err)
	}
	if text != "standup with @bob #meeting" {
		t.Errorf("event text = %q, want %q", text, "standup with @bob #meeting")
	}

	var metaCount int
	err = database.QueryRow("SELECT COUNT(*) FROM event_meta WHERE event_id = ?", id).Scan(&metaCount)
	if err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if metaCount != 3 {
		t.Errorf("meta count = %d, want 3", metaCount)
	}

	var ftsCount int
	err = database.QueryRow("SELECT COUNT(*) FROM events_fts WHERE events_fts MATCH ?", "standup").Scan(&ftsCount)
	if err != nil {
		t.Fatalf("FTS query: %v", err)
	}
	if ftsCount != 1 {
		t.Errorf("FTS match count = %d, want 1", ftsCount)
	}
}

func TestAdd_WithParent(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	parentID, err := Add(ctx, database, AddInput{Title: "parent event"})
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}

	childID, err := Add(ctx, database, AddInput{Title: "child event", ParentID: &parentID})
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	var storedParentID int64
	err = database.QueryRow("SELECT parent_id FROM events WHERE id = ?", childID).Scan(&storedParentID)
	if err != nil {
		t.Fatalf("query child parent_id: %v", err)
	}
	if storedParentID != parentID {
		t.Errorf("parent_id = %d, want %d", storedParentID, parentID)
	}
}

func TestAdd_InvalidParent(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	invalidParent := int64(9999)
	_, err := Add(ctx, database, AddInput{Title: "orphan event", ParentID: &invalidParent})
	if err == nil {
		t.Fatal("expected error for invalid parent, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

// TestAdd_RejectsBadAuthorMeta pins the single-author invariant at the writer
// rather than only in MergeMeta. The data layer refuses every attempt to
// repair an event's author, so it has to refuse to create the states that
// would need repairing — including from an AddInput built directly, which is
// the one path MergeMeta does not sit in front of.
func TestAdd_RejectsBadAuthorMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		meta    []parse.Meta
		wantErr string
	}{
		{
			name: "two distinct authors",
			meta: []parse.Meta{
				{Key: MetaKeyAuthor, Value: "nicolas"},
				{Key: MetaKeyAuthor, Value: "bob"},
			},
			wantErr: "exactly one author",
		},
		{
			name:    "blank author",
			meta:    []parse.Meta{{Key: MetaKeyAuthor, Value: ""}},
			wantErr: "author cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)

			_, err := Add(ctx, database, AddInput{Title: "note", Meta: tt.meta})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Add err = %v, want one containing %q", err, tt.wantErr)
			}

			var count int
			if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
				t.Fatalf("count: %v", err)
			}
			if count != 0 {
				t.Errorf("created %d events, want 0", count)
			}
		})
	}
}

func TestAddMany_Empty(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	ids, err := AddMany(ctx, database, nil)
	if err != nil {
		t.Fatalf("AddMany(nil): %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil for empty input", ids)
	}

	ids, err = AddMany(ctx, database, []AddInput{})
	if err != nil {
		t.Fatalf("AddMany([]): %v", err)
	}
	if ids != nil {
		t.Errorf("ids = %v, want nil for empty input", ids)
	}

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("created %d rows, want 0", count)
	}
}

func TestAddMany_HappyPath(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	inputs := []AddInput{
		{Title: "a", Meta: []parse.Meta{{Key: "tag", Value: "x"}}},
		{Title: "b"},
		{Title: "c", Meta: []parse.Meta{{Key: "tag", Value: "y"}, {Key: "people", Value: "alice"}}},
	}
	ids, err := AddMany(ctx, database, inputs)
	if err != nil {
		t.Fatalf("AddMany: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d ids, want 3", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("ids not strictly increasing: %v", ids)
		}
	}

	for i, id := range ids {
		ev, err := Get(ctx, database, id)
		if err != nil {
			t.Fatalf("Get(%d): %v", id, err)
		}
		if ev.Title != inputs[i].Title {
			t.Errorf("event %d text = %q, want %q", id, ev.Title, inputs[i].Title)
		}
		if len(ev.Meta) != len(inputs[i].Meta) {
			t.Errorf("event %d meta count = %d, want %d", id, len(ev.Meta), len(inputs[i].Meta))
		}
	}
}

func TestAddMany_AtomicOnError(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	bogusParent := int64(9999)
	inputs := []AddInput{
		{Title: "good 1"},
		{Title: "good 2"},
		{Title: "bad", ParentID: &bogusParent}, // parent does not exist
		{Title: "good 3"},
	}
	_, err := AddMany(ctx, database, inputs)
	if err == nil {
		t.Fatal("AddMany returned nil err, want parent-not-found")
	}

	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("created %d rows, want 0 (batch should roll back atomically)", count)
	}
}

func TestAddMany_FTSPopulatedPerRecord(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	inputs := []AddInput{
		{Title: "deploy ops", Meta: []parse.Meta{{Key: "tag", Value: "ops"}}},
		{Title: "lunch chat", Meta: []parse.Meta{{Key: "tag", Value: "personal"}}},
	}
	if _, err := AddMany(ctx, database, inputs); err != nil {
		t.Fatalf("AddMany: %v", err)
	}

	matches, err := List(ctx, database, ListOpts{Filter: "deploy"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(matches) != 1 || matches[0].Title != "deploy ops" {
		t.Errorf("FTS not populated: matches = %v", matches)
	}
}

// TestAddMany_ParentIndex covers batch-relative parents in both directions.
// The forward reference is the interesting one: `fngr --format=json` emits
// newest-first, so on a re-import a child is almost always inserted before the
// parent it points at.
func TestAddMany_ParentIndex(t *testing.T) {
	t.Parallel()

	idx := func(i int) *int { return &i }
	tests := []struct {
		name   string
		inputs []AddInput
		// want maps a title to its expected parent's title ("" = root).
		want map[string]string
	}{
		{
			name: "parent precedes child",
			inputs: []AddInput{
				{Title: "root"},
				{Title: "child", ParentIndex: idx(0)},
			},
			want: map[string]string{"root": "", "child": "root"},
		},
		{
			name: "child precedes parent",
			inputs: []AddInput{
				{Title: "child", ParentIndex: idx(1)},
				{Title: "root"},
			},
			want: map[string]string{"root": "", "child": "root"},
		},
		{
			name: "chain in reverse order",
			inputs: []AddInput{
				{Title: "grandchild", ParentIndex: idx(1)},
				{Title: "child", ParentIndex: idx(2)},
				{Title: "root"},
			},
			want: map[string]string{"root": "", "child": "root", "grandchild": "child"},
		},
		{
			name: "two children share one parent",
			inputs: []AddInput{
				{Title: "a", ParentIndex: idx(2)},
				{Title: "b", ParentIndex: idx(2)},
				{Title: "root"},
			},
			want: map[string]string{"root": "", "a": "root", "b": "root"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			ids, err := AddMany(ctx, database, tt.inputs)
			if err != nil {
				t.Fatalf("AddMany: %v", err)
			}

			titleByID := make(map[int64]string, len(ids))
			for i, id := range ids {
				titleByID[id] = tt.inputs[i].Title
			}
			for i, id := range ids {
				ev, err := Get(ctx, database, id)
				if err != nil {
					t.Fatalf("Get(%d): %v", id, err)
				}
				gotParent := ""
				if ev.ParentID != nil {
					gotParent = titleByID[*ev.ParentID]
				}
				if want := tt.want[tt.inputs[i].Title]; gotParent != want {
					t.Errorf("%q parent = %q, want %q", tt.inputs[i].Title, gotParent, want)
				}
			}
		})
	}
}

func TestAddMany_ParentIndexRejected(t *testing.T) {
	t.Parallel()

	idx := func(i int) *int { return &i }
	id := int64(1)
	tests := []struct {
		name    string
		inputs  []AddInput
		wantMsg string
	}{
		{"negative", []AddInput{{Title: "a", ParentIndex: idx(-1)}}, "out of range"},
		{"past the end", []AddInput{{Title: "a", ParentIndex: idx(1)}}, "out of range"},
		{"self reference", []AddInput{{Title: "a", ParentIndex: idx(0)}}, "cycle"},
		{
			"two-record cycle",
			[]AddInput{
				{Title: "a", ParentIndex: idx(1)},
				{Title: "b", ParentIndex: idx(0)},
			},
			"cycle",
		},
		{
			// A cycle reachable only from a record that is itself fine, to
			// prove the scan does not stop at the first terminating walk.
			"cycle behind a valid record",
			[]AddInput{
				{Title: "ok"},
				{Title: "a", ParentIndex: idx(2)},
				{Title: "b", ParentIndex: idx(1)},
			},
			"cycle",
		},
		{
			"both parent forms set",
			[]AddInput{{Title: "root"}, {Title: "a", ParentID: &id, ParentIndex: idx(0)}},
			"mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			_, err := AddMany(ctx, database, tt.inputs)
			if err == nil {
				t.Fatal("AddMany succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("err = %q, want it to contain %q", err, tt.wantMsg)
			}

			// Validation runs before the first INSERT, so nothing is written.
			var count int
			if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
				t.Fatalf("count: %v", err)
			}
			if count != 0 {
				t.Errorf("created %d rows, want 0", count)
			}
		})
	}
}

func TestAdd_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	if _, err := Add(context.Background(), database, AddInput{Title: ""}); err == nil {
		t.Error("Add with empty title: expected error")
	}
}

// TestAddMany_PerRecordErrorsNameTheRecord covers the hoist of the per-record
// checks out of the insert loop and into validateAddInputs. A --format=json
// import runs to 10 000 records, so `title cannot be empty` with no index is
// a message nobody can act on. The parent-existence check stays in the loop
// (it needs the transaction) and is indexed there for the same reason. A
// one-record batch carries no index at all — see recordPrefix.
func TestAddMany_PerRecordErrorsNameTheRecord(t *testing.T) {
	t.Parallel()

	missing := int64(9999)
	tests := []struct {
		name    string
		inputs  []AddInput
		wantMsg string
		noIndex bool // the message must not name a record at all
	}{
		{
			name:    "empty title",
			inputs:  []AddInput{{Title: "ok"}, {Title: "ok"}, {Title: ""}},
			wantMsg: `record 2: title cannot be empty`,
		},
		{
			name: "two authors",
			inputs: []AddInput{{Title: "ok"}, {Title: "b", Meta: []parse.Meta{
				{Key: MetaKeyAuthor, Value: "alice"},
				{Key: MetaKeyAuthor, Value: "bob"},
			}}},
			wantMsg: `record 1: `,
		},
		{
			name:    "unstorable meta",
			inputs:  []AddInput{{Title: "a", Meta: []parse.Meta{{Key: "k", Value: ""}}}, {Title: "b"}},
			wantMsg: `record 0: `,
		},
		{
			name:    "parent that does not exist",
			inputs:  []AddInput{{Title: "ok"}, {Title: "b", ParentID: &missing}},
			wantMsg: `record 1: parent event 9999`,
		},
		{
			name:    "lone record is not numbered",
			inputs:  []AddInput{{Title: ""}},
			wantMsg: `title cannot be empty`,
			noIndex: true,
		},
		{
			name:    "lone record with a missing parent is not numbered either",
			inputs:  []AddInput{{Title: "b", ParentID: &missing}},
			wantMsg: `parent event 9999`,
			noIndex: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)

			_, err := AddMany(ctx, database, tt.inputs)
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("AddMany err = %v, want one containing %q", err, tt.wantMsg)
			}
			if tt.noIndex && strings.Contains(err.Error(), "record ") {
				t.Errorf("AddMany err = %v, want no record index on a one-record batch", err)
			}

			// The whole batch rolls back, whichever half caught it.
			var count int
			if err := database.QueryRow("SELECT COUNT(*) FROM events").Scan(&count); err != nil {
				t.Fatalf("count: %v", err)
			}
			if count != 0 {
				t.Errorf("created %d events, want 0", count)
			}
		})
	}
}

// TestAdd_RejectsOutOfRangeTimestamp is the storage-layer backstop for C2.
// timefmt rejects absurd relative offsets at parse time, but a CreatedAt can
// also arrive from --format=json or from a caller building AddInput directly,
// and a year outside 1-9999 formats to a string the driver cannot read back.
// The bounds themselves are TestInRange's job; this only proves Add consults
// them and writes nothing when they fail.
func TestAdd_RejectsOutOfRangeTimestamp(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	bad := time.Date(-712, 8, 29, 14, 7, 26, 0, time.UTC)
	if _, err := Add(ctx, database, AddInput{Title: "x", CreatedAt: &bad}); !errors.Is(err, ErrTimeRange) {
		t.Fatalf("Add: err = %v, want ErrTimeRange", err)
	}
	events, err := List(ctx, database, ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("rejected Add left %d events behind", len(events))
	}
}
