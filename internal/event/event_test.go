package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

var ctx = context.Background()

// testDB returns a fresh per-test database backed by a temporary file. We
// avoid SQLite's bare `:memory:` URI because each connection in a *sql.DB
// pool sees its own empty in-memory database — which breaks any function
// that runs a follow-up query (e.g. loadMetaBatch) on a second connection
// while the first still holds an open Rows iterator.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fngr.db")
	database, err := db.Open(path, true)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

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

func TestGet(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	meta := []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}

	id, err := Add(ctx, database, AddInput{Title: "get me", Meta: meta})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if ev.Title != "get me" {
		t.Errorf("event.Title = %q, want %q", ev.Title, "get me")
	}
	if len(ev.Meta) != 2 {
		t.Errorf("len(event.Meta) = %d, want 2", len(ev.Meta))
	}
}

func TestGet_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Get(ctx, database, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "to be deleted"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := Delete(ctx, database, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", id).Scan(&count)
	if err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected event to be deleted, got count=%d", count)
	}
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	err := Delete(ctx, database, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestUpdate_TextOnly(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "old text", Meta: []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "ops"},
	}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newText := "new text"
	if err := Update(ctx, database, id, &newText, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != newText {
		t.Errorf("text = %q, want %q", ev.Title, newText)
	}

	matches, err := List(ctx, database, ListOpts{Filter: "new"})
	if err != nil {
		t.Fatalf("List filter new: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("FTS not updated: got %d matches for 'new', want 1", len(matches))
	}
	matches, err = List(ctx, database, ListOpts{Filter: "old"})
	if err != nil {
		t.Fatalf("List filter old: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("stale FTS row: got %d matches for 'old', want 0", len(matches))
	}
}

func TestUpdate_TimeOnly(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "stamped"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	if err := Update(ctx, database, id, nil, nil, &newTime); err != nil {
		t.Fatalf("Update: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ev.CreatedAt.Equal(newTime) {
		t.Errorf("created_at = %v, want %v", ev.CreatedAt, newTime)
	}
}

func TestUpdate_NoOp(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	if err := Update(ctx, database, 9999, nil, nil, nil); err != nil {
		t.Errorf("no-op Update returned error: %v", err)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	newText := "x"
	err := Update(ctx, database, 9999, &newText, nil, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestDelete_CascadesChildren(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	parentID, err := Add(ctx, database, AddInput{Title: "parent", Meta: []parse.Meta{{Key: "author", Value: "alice"}}})
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}

	childID, err := Add(ctx, database, AddInput{Title: "child", ParentID: &parentID, Meta: []parse.Meta{{Key: "tag", Value: "reply"}}})
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	if err := Delete(ctx, database, parentID); err != nil {
		t.Fatalf("Delete parent: %v", err)
	}

	var count int
	err = database.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", childID).Scan(&count)
	if err != nil {
		t.Fatalf("count child: %v", err)
	}
	if count != 0 {
		t.Errorf("expected child event to be cascade-deleted, got count=%d", count)
	}
}

func TestList_NoFilter(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, AddInput{Title: "first event #work", Meta: []parse.Meta{{Key: "tag", Value: "work"}}})
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	_, err = Add(ctx, database, AddInput{Title: "second event #personal", Meta: []parse.Meta{{Key: "tag", Value: "personal"}}})
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	events, err := List(ctx, database, ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Title != "second event #personal" {
		t.Errorf("events[0].Title = %q, want %q", events[0].Title, "second event #personal")
	}
	if events[1].Title != "first event #work" {
		t.Errorf("events[1].Title = %q, want %q", events[1].Title, "first event #work")
	}
}

func TestList_WithDateRange(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := database.Exec("INSERT INTO events (title, created_at) VALUES (?, ?)", "old event", "2026-01-01 00:00:00")
	if err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	_, err = database.Exec("INSERT INTO events_fts (rowid, content) VALUES (1, 'old event')")
	if err != nil {
		t.Fatalf("insert old FTS: %v", err)
	}

	_, err = database.Exec("INSERT INTO events (title, created_at) VALUES (?, ?)", "new event", "2026-03-15 12:00:00")
	if err != nil {
		t.Fatalf("insert new event: %v", err)
	}
	_, err = database.Exec("INSERT INTO events_fts (rowid, content) VALUES (2, 'new event')")
	if err != nil {
		t.Fatalf("insert new FTS: %v", err)
	}

	mar1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	feb2 := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	feb1 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	apr2 := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)

	events, err := List(ctx, database, ListOpts{From: &mar1})
	if err != nil {
		t.Fatalf("List with From: %v", err)
	}
	if len(events) != 1 || events[0].Title != "new event" {
		t.Errorf("From only got %d events; want [new event]", len(events))
	}

	events, err = List(ctx, database, ListOpts{To: &feb2})
	if err != nil {
		t.Fatalf("List with To: %v", err)
	}
	if len(events) != 1 || events[0].Title != "old event" {
		t.Errorf("To only got %d events; want [old event]", len(events))
	}

	events, err = List(ctx, database, ListOpts{From: &feb1, To: &apr2})
	if err != nil {
		t.Fatalf("List with From and To: %v", err)
	}
	if len(events) != 1 || events[0].Title != "new event" {
		t.Errorf("From+To got %d events; want [new event]", len(events))
	}
}

func TestCountMeta(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	for range 3 {
		if _, err := Add(ctx, database, AddInput{Title: "evt", Meta: []parse.Meta{
			{Key: MetaKeyTag, Value: "ops"},
		}}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if _, err := Add(ctx, database, AddInput{Title: "other", Meta: []parse.Meta{
		{Key: MetaKeyTag, Value: "work"},
	}}); err != nil {
		t.Fatalf("Add other: %v", err)
	}

	got, err := CountMeta(ctx, database, MetaKeyTag, "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if got != 3 {
		t.Errorf("CountMeta(tag, ops) = %d, want 3", got)
	}

	got, err = CountMeta(ctx, database, MetaKeyTag, "missing")
	if err != nil {
		t.Fatalf("CountMeta missing: %v", err)
	}
	if got != 0 {
		t.Errorf("CountMeta(tag, missing) = %d, want 0", got)
	}
}

// TestProtectedMetaKey_RefusedByEveryVerb is the M3 guard. `author` is
// single-valued and written only by Add, so every verb that could add a
// second one, remove the only one, or rename another key onto it must
// refuse. Each subtest asserts the event still carries exactly one
// unchanged author afterwards — an error return that still mutated would
// be the worse failure.
func TestProtectedMetaKey_RefusedByEveryVerb(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call func(ctx context.Context, db *sql.DB, id int64) error
	}{
		{"update-meta-source", func(ctx context.Context, db *sql.DB, id int64) error {
			_, err := UpdateMeta(ctx, db, MetaKeyAuthor, "alice", "k", "v")
			return err
		}},
		{"update-meta-target", func(ctx context.Context, db *sql.DB, id int64) error {
			// The pre-M3 guard read oldKey only, so this minted a second
			// author on every event carrying project=fngr.
			_, err := UpdateMeta(ctx, db, "project", "fngr", MetaKeyAuthor, "evil")
			return err
		}},
		{"delete-meta", func(ctx context.Context, db *sql.DB, id int64) error {
			_, err := DeleteMeta(ctx, db, MetaKeyAuthor, "alice")
			return err
		}},
		{"add-tags", func(ctx context.Context, db *sql.DB, id int64) error {
			_, err := AddTags(ctx, db, id, []parse.Meta{{Key: MetaKeyAuthor, Value: "zzz"}})
			return err
		}},
		{"add-tags-alongside-a-legal-one", func(ctx context.Context, db *sql.DB, id int64) error {
			// Rejected as a whole: a partial apply would be worse than
			// either outcome.
			_, err := AddTags(ctx, db, id, []parse.Meta{
				{Key: MetaKeyTag, Value: "ok"},
				{Key: MetaKeyAuthor, Value: "zzz"},
			})
			return err
		}},
		{"remove-tags", func(ctx context.Context, db *sql.DB, id int64) error {
			_, err := RemoveTags(ctx, db, id, []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			id, err := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{
				{Key: MetaKeyAuthor, Value: "alice"},
				{Key: "project", Value: "fngr"},
			}})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}

			if err := tc.call(ctx, database, id); err == nil ||
				!strings.Contains(err.Error(), "single-valued") {
				t.Fatalf("err = %v, want a protected-key rejection", err)
			}

			ev, err := Get(ctx, database, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			var authors []string
			for _, m := range ev.Meta {
				if m.Key == MetaKeyAuthor {
					authors = append(authors, m.Value)
				}
			}
			if len(authors) != 1 || authors[0] != "alice" {
				t.Errorf("authors = %v, want exactly [alice]", authors)
			}
		})
	}
}

func TestUpdateMeta_ResyncsFTS(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	for range 2 {
		if _, err := Add(ctx, database, AddInput{Title: "deploy", Meta: []parse.Meta{
			{Key: MetaKeyTag, Value: "ops"},
		}}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	n, err := UpdateMeta(ctx, database, MetaKeyTag, "ops", MetaKeyTag, "infra")
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	if n != 2 {
		t.Errorf("UpdateMeta rows = %d, want 2", n)
	}

	// FTS index must reflect the rename: #infra finds both, #ops finds none.
	infra, err := List(ctx, database, ListOpts{Filter: "#infra"})
	if err != nil {
		t.Fatalf("List #infra: %v", err)
	}
	if len(infra) != 2 {
		t.Errorf("search #infra = %d events, want 2 (FTS stale after rename)", len(infra))
	}
	ops, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List #ops: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("search #ops = %d events, want 0 (FTS stale after rename)", len(ops))
	}
}

// TestUpdateMeta_Merges covers the rename that lands on a tuple some event
// already carries — consolidating two tags, which is the main reason to run
// the verb at all. The UNIQUE(key, value, event_id) index makes that a
// collision, and a plain UPDATE would abort the whole transaction, leaving
// even the non-colliding events untouched. `OR IGNORE` alone is not enough
// either: it would leave the losing row behind under the old tuple.
func TestUpdateMeta_Merges(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	// Event 1 carries only the old tag; event 2 carries both, so its
	// existing #done row is the one standing in the way.
	if _, err := Add(ctx, database, AddInput{Title: "alpha", Meta: []parse.Meta{
		{Key: MetaKeyTag, Value: "wip"},
	}}); err != nil {
		t.Fatalf("Add alpha: %v", err)
	}
	if _, err := Add(ctx, database, AddInput{Title: "beta", Meta: []parse.Meta{
		{Key: MetaKeyTag, Value: "wip"},
		{Key: MetaKeyTag, Value: "done"},
	}}); err != nil {
		t.Fatalf("Add beta: %v", err)
	}

	n, err := UpdateMeta(ctx, database, MetaKeyTag, "wip", MetaKeyTag, "done")
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	if n != 2 {
		t.Errorf("UpdateMeta = %d, want 2 (both tuples accounted for)", n)
	}

	// One #done per event, no #wip anywhere, and no duplicate row on event 2.
	counts, err := ListMeta(ctx, database, ListMetaOpts{Key: MetaKeyTag})
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(counts) != 1 {
		t.Fatalf("ListMeta = %+v, want a single tag=done entry", counts)
	}
	if counts[0].Value != "done" || counts[0].Count != 2 {
		t.Errorf("ListMeta = %s=%s (%d), want tag=done (2)",
			counts[0].Key, counts[0].Value, counts[0].Count)
	}

	// The FTS index has to agree for event 2 as well, whose row was replaced
	// rather than updated. TestUpdateMeta_ResyncsFTS already owns the
	// straightforward direction, so only #wip is checked here.
	wip, err := List(ctx, database, ListOpts{Filter: "#wip"})
	if err != nil {
		t.Fatalf("List #wip: %v", err)
	}
	if len(wip) != 0 {
		t.Errorf("search #wip = %d events, want 0", len(wip))
	}
}

// TestUpdateMeta_RenameToItself pins the degenerate case. `UPDATE OR REPLACE`
// handles it for free — SQLite checks uniqueness against the other rows, so
// the row is simply rewritten with the values it already had — but every
// two-statement formulation of the merge gets it wrong, deleting exactly the
// rows the update just wrote. The test is here so the next rewrite finds out.
func TestUpdateMeta_RenameToItself(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	if _, err := Add(ctx, database, AddInput{Title: "alpha", Meta: []parse.Meta{
		{Key: MetaKeyTag, Value: "ops"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	n, err := UpdateMeta(ctx, database, MetaKeyTag, "ops", MetaKeyTag, "ops")
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	if n != 1 {
		t.Errorf("UpdateMeta = %d, want 1", n)
	}

	got, err := CountMeta(ctx, database, MetaKeyTag, "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if got != 1 {
		t.Errorf("CountMeta(tag, ops) = %d, want 1 — the tag was deleted", got)
	}
}

func TestDeleteMeta_ResyncsFTS(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	if _, err := Add(ctx, database, AddInput{Title: "deploy", Meta: []parse.Meta{
		{Key: MetaKeyTag, Value: "ops"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	n, err := DeleteMeta(ctx, database, MetaKeyTag, "ops")
	if err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	if n != 1 {
		t.Errorf("DeleteMeta rows = %d, want 1", n)
	}

	// FTS index must reflect the deletion: #ops finds nothing.
	ops, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List #ops: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("search #ops = %d events, want 0 (FTS stale after delete)", len(ops))
	}
}

func TestHasChildren_False(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "lonely"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	has, err := HasChildren(ctx, database, id)
	if err != nil {
		t.Fatalf("HasChildren: %v", err)
	}
	if has {
		t.Error("HasChildren = true, want false")
	}
}

func TestListMeta(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, AddInput{Title: "event one", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}})
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	_, err = Add(ctx, database, AddInput{Title: "event two", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "personal"},
	}})
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	counts, err := ListMeta(ctx, database, ListMetaOpts{})
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}

	if len(counts) != 3 {
		t.Fatalf("len(counts) = %d, want 3", len(counts))
	}

	expected := []struct {
		key   string
		value string
		count int
	}{
		{"author", "alice", 2},
		{"tag", "personal", 1},
		{"tag", "work", 1},
	}

	for i, exp := range expected {
		if counts[i].Key != exp.key || counts[i].Value != exp.value || counts[i].Count != exp.count {
			t.Errorf("counts[%d] = {%q, %q, %d}, want {%q, %q, %d}",
				i, counts[i].Key, counts[i].Value, counts[i].Count,
				exp.key, exp.value, exp.count)
		}
	}
}

func TestListMeta_FilterByKey(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	if _, err := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "tag", Value: "deploy"},
		{Key: "people", Value: "alice"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := ListMeta(ctx, database, ListMetaOpts{Key: "tag"})
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %v", len(got), got)
	}
	for _, mc := range got {
		if mc.Key != "tag" {
			t.Errorf("got key %q, want tag", mc.Key)
		}
	}
}

func TestListMeta_FilterByKeyValue(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	for range 3 {
		if _, err := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{
			{Key: "tag", Value: "ops"},
		}}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if _, err := Add(ctx, database, AddInput{Title: "y", Meta: []parse.Meta{
		{Key: "tag", Value: "other"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := ListMeta(ctx, database, ListMetaOpts{Key: "tag", Value: "ops"})
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %v", len(got), got)
	}
	if got[0].Key != "tag" || got[0].Value != "ops" || got[0].Count != 3 {
		t.Errorf("got %+v, want tag=ops count=3", got[0])
	}
}

func TestListMeta_FilterEmptyResult(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	if _, err := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
	}}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := ListMeta(ctx, database, ListMetaOpts{Key: "tag", Value: "missing"})
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d rows, want 0: %v", len(got), got)
	}
}

func TestGetSubtree(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	root, err := Add(ctx, database, AddInput{Title: "root", Meta: []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}})
	if err != nil {
		t.Fatalf("Add root: %v", err)
	}

	child, err := Add(ctx, database, AddInput{Title: "child", ParentID: &root, Meta: []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}})
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	grandchild, err := Add(ctx, database, AddInput{Title: "grandchild", ParentID: &child, Meta: []parse.Meta{{Key: MetaKeyAuthor, Value: "bob"}}})
	if err != nil {
		t.Fatalf("Add grandchild: %v", err)
	}

	if _, err := Add(ctx, database, AddInput{Title: "unrelated"}); err != nil {
		t.Fatalf("Add unrelated: %v", err)
	}

	events, err := GetSubtree(ctx, database, root)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}

	ids := idsOf(events)
	if ids[0] != root || ids[1] != child || ids[2] != grandchild {
		t.Errorf("subtree IDs = %v, want [%d %d %d]", ids, root, child, grandchild)
	}

	if len(events[2].Meta) != 1 || events[2].Meta[0].Value != "bob" {
		t.Errorf("grandchild meta = %v, want [{author bob}]", events[2].Meta)
	}
}

func TestGetSubtree_LeafNode(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "leaf"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	events, err := GetSubtree(ctx, database, id)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Title != "leaf" {
		t.Errorf("events[0].Title = %q, want %q", events[0].Title, "leaf")
	}
}

// TestGetSubtree_CorruptChainTerminates is the M2 CTE fix. `fngr event 1 -t`
// on a cyclic chain used to spin with no output and no error until killed.
// Terminating is only half of it — the query must also refuse rather than
// present a deduped loop as though it were a subtree.
func TestGetSubtree_CorruptChainTerminates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func(t *testing.T, database *sql.DB) int64
	}{
		{
			name: "two events pointing at each other",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
				forgeParent(t, database, a, b)
				return a
			},
		},
		{
			name: "self-parent",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				forgeParent(t, database, a, a)
				return a
			},
		},
		{
			// A well-formed subtree hangs off the cycle, so the recursion
			// fans out as well as looping and the result is more than just
			// the loop. Only a cycle member can be the root of a run that
			// loops: every member's parent is another member, so nothing
			// outside the cycle can descend into one.
			name: "cycle with a clean subtree hanging off it",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
				bystander, _ := Add(ctx, database, AddInput{Title: "bystander", ParentID: &a})
				if _, err := Add(ctx, database, AddInput{Title: "leaf", ParentID: &bystander}); err != nil {
					t.Fatalf("Add leaf: %v", err)
				}
				forgeParent(t, database, a, b)
				return a
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			root := tt.build(t, database)

			_, err := GetSubtree(boundedCtx(t), database, root)
			if !errors.Is(err, ErrCorruptTree) {
				t.Errorf("err = %v, want ErrCorruptTree", err)
			}
		})
	}
}

// TestGetSubtree_DeepChainIsNotTruncated pins the other half: nothing legitimate
// may be lost to the cycle defence. The whole table is one chain — the deepest
// recursion the row count allows — and every event must still come back. A
// defence that quietly dropped the last generation would be the blackout M2 is
// about, moved one level down.
func TestGetSubtree_DeepChainIsNotTruncated(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	const depth = 40
	var root, parent int64
	for i := range depth {
		in := AddInput{Title: fmt.Sprintf("e%d", i)}
		if i > 0 {
			in.ParentID = &parent
		}
		id, err := Add(ctx, database, in)
		if err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
		if i == 0 {
			root = id
		}
		parent = id
	}

	events, err := GetSubtree(ctx, database, root)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}
	if len(events) != depth {
		t.Errorf("len(events) = %d, want %d", len(events), depth)
	}
}

func TestGetSubtree_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := GetSubtree(ctx, database, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent root, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

// TestCountSubtree pins CountSubtree against GetSubtree: it exists only to
// avoid materializing rows nobody reads, so the number it returns must be the
// one a caller would have got from len(GetSubtree(...)).
func TestCountSubtree(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func(t *testing.T, database *sql.DB) int64
		want  int64
	}{
		{
			name: "leaf counts itself",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				if _, err := Add(ctx, database, AddInput{Title: "unrelated"}); err != nil {
					t.Fatalf("Add unrelated: %v", err)
				}
				return a
			},
			want: 1,
		},
		{
			name: "root, child and grandchild",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
				if _, err := Add(ctx, database, AddInput{Title: "c", ParentID: &b}); err != nil {
					t.Fatalf("Add c: %v", err)
				}
				if _, err := Add(ctx, database, AddInput{Title: "unrelated"}); err != nil {
					t.Fatalf("Add unrelated: %v", err)
				}
				return a
			},
			want: 3,
		},
		{
			// Counted from halfway down: an ancestor is not a descendant.
			name: "mid-chain root excludes what is above it",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
				if _, err := Add(ctx, database, AddInput{Title: "c", ParentID: &b}); err != nil {
					t.Fatalf("Add c: %v", err)
				}
				return b
			},
			want: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			root := tt.build(t, database)

			got, err := CountSubtree(ctx, database, root)
			if err != nil {
				t.Fatalf("CountSubtree: %v", err)
			}
			if got != tt.want {
				t.Errorf("CountSubtree = %d, want %d", got, tt.want)
			}
			events, err := GetSubtree(ctx, database, root)
			if err != nil {
				t.Fatalf("GetSubtree: %v", err)
			}
			if got != int64(len(events)) {
				t.Errorf("CountSubtree = %d but len(GetSubtree) = %d; the two must agree", got, len(events))
			}
		})
	}
}

// TestCountSubtree_CorruptChainTerminates mirrors GetSubtree's cycle defence.
// Counting is a walk too, so it needs the same UNION and the same loop test —
// terminating on a cyclic chain is not the same as answering for it.
func TestCountSubtree_CorruptChainTerminates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func(t *testing.T, database *sql.DB) int64
	}{
		{
			name: "two events pointing at each other",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
				forgeParent(t, database, a, b)
				return a
			},
		},
		{
			name: "self-parent",
			build: func(t *testing.T, database *sql.DB) int64 {
				a, _ := Add(ctx, database, AddInput{Title: "a"})
				forgeParent(t, database, a, a)
				return a
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			root := tt.build(t, database)

			_, err := CountSubtree(boundedCtx(t), database, root)
			if !errors.Is(err, ErrCorruptTree) {
				t.Errorf("err = %v, want ErrCorruptTree", err)
			}
		})
	}
}

// TestCountSubtree_AncestorCycleIsNotTheRootsProblem is the other half: a cycle
// sitting above the queried root leaves its subtree well-formed, and the count
// must come back rather than refuse. GetSubtree draws the line in the same
// place, and delete -r depends on it — the escape hatch out of a corrupt tree
// must still be able to name what it is taking.
func TestCountSubtree_AncestorCycleIsNotTheRootsProblem(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	a, _ := Add(ctx, database, AddInput{Title: "a"})
	b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
	c, err := Add(ctx, database, AddInput{Title: "c", ParentID: &b})
	if err != nil {
		t.Fatalf("Add c: %v", err)
	}
	if _, err := Add(ctx, database, AddInput{Title: "d", ParentID: &c}); err != nil {
		t.Fatalf("Add d: %v", err)
	}
	forgeParent(t, database, a, b) // a <-> b, entirely above c

	got, err := CountSubtree(boundedCtx(t), database, c)
	if err != nil {
		t.Fatalf("CountSubtree: %v", err)
	}
	if got != 2 {
		t.Errorf("CountSubtree = %d, want 2", got)
	}
}

func TestCountSubtree_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := CountSubtree(ctx, database, 9999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestFTSIsolation_MetaTokensNotMatchedByBareWords(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, AddInput{Title: "pushed to production", Meta: []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "deploy"},
	}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	events, err := List(ctx, database, ListOpts{Filter: "deploy"})
	if err != nil {
		t.Fatalf("List bare word: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("bare word 'deploy' matched %d events, want 0 (FTS isolation broken)", len(events))
	}

	events, err = List(ctx, database, ListOpts{Filter: "#deploy"})
	if err != nil {
		t.Fatalf("List #deploy: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("#deploy matched %d events, want 1", len(events))
	}
}

func TestFTSIsolation_BodyWordsNotMatchedByMetaFilter(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, AddInput{Title: "heading to work early", Meta: []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
	}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	events, err := List(ctx, database, ListOpts{Filter: "#work"})
	if err != nil {
		t.Fatalf("List #work: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("#work matched %d events, want 0 (body word leaked into meta filter)", len(events))
	}

	events, err = List(ctx, database, ListOpts{Filter: "work"})
	if err != nil {
		t.Fatalf("List bare work: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("bare 'work' matched %d events, want 1", len(events))
	}
}

// TestFTSIsolation_TextCannotForgeAMetaMatch covers M7. Content and metadata
// shared one FTS column, so a note *saying* `secret=classified` produced the
// same token as the event *tagged* that way: any import could write itself
// into a tag view, and `!` could write itself out of one.
func TestFTSIsolation_TextCannotForgeAMetaMatch(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	tagged, err := Add(ctx, database, AddInput{Title: "real note", Meta: []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: "secret", Value: "classified"},
	}})
	if err != nil {
		t.Fatalf("Add tagged: %v", err)
	}
	forged, err := Add(ctx, database, AddInput{
		Title: "fake note",
		Body:  "mentions secret=classified and tag=ops literally",
		Meta:  []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}},
	})
	if err != nil {
		t.Fatalf("Add forged: %v", err)
	}

	for _, tt := range []struct {
		name   string
		filter string
		want   []int64
	}{
		{"key=value matches only the tagged event", "secret=classified", []int64{tagged}},
		{"shorthand matches nothing at all", "#ops", nil},
		{"negation cannot be forged either", "!secret=classified", []int64{forged}},
		// The text is still findable as text — the fix scopes the search, it
		// does not stop indexing the body.
		{"the words stay searchable as content", "literally", []int64{forged}},
		{"so does the forged token", "secret=classified literally", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			events, err := List(ctx, database, ListOpts{Filter: tt.filter})
			if err != nil {
				t.Fatalf("List %q: %v", tt.filter, err)
			}
			if got := idsOf(events); !slices.Equal(got, tt.want) {
				t.Errorf("List(%q) = %v, want %v", tt.filter, got, tt.want)
			}
		})
	}
}

func TestListSeq_PropagatesDBError(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	if _, err := Add(ctx, database, AddInput{Title: "ok"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	calls := 0
	var lastErr error
	for ev, err := range ListSeq(ctx, database, ListOpts{}) {
		calls++
		if err == nil {
			t.Fatalf("yield #%d returned (%+v, nil), want non-nil error", calls, ev)
		}
		if ev.ID != 0 || ev.Title != "" || ev.ParentID != nil || len(ev.Meta) != 0 {
			t.Errorf("yield #%d returned non-zero event with err: %+v", calls, ev)
		}
		lastErr = err
	}
	if calls != 1 {
		t.Errorf("got %d yields after error, want exactly 1", calls)
	}
	if lastErr == nil {
		t.Error("expected an error to be yielded against a closed DB")
	}
}

// TestList_NegativeLimitRefused pins the store half of the guard. Only
// `Limit > 0` emits a LIMIT clause, so a negative value used to widen the
// result to the whole journal — the opposite of what a limit is for, and
// silent. The CLI checks too, but a directly-built ListOpts skips that.
func TestList_NegativeLimitRefused(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	if _, err := Add(ctx, database, AddInput{Title: "x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := List(ctx, database, ListOpts{Limit: -1}); err == nil {
		t.Error("List accepted Limit: -1")
	}

	// ListSeq compiles the same query, so it has to refuse it too — lazily,
	// on the first read.
	for _, err := range ListSeq(ctx, database, ListOpts{Limit: -1}) {
		if err == nil {
			t.Error("ListSeq yielded an event for Limit: -1")
		}
		break
	}
}

func TestList_LimitAndSort(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	for i := range 5 {
		_, err := database.Exec(
			"INSERT INTO events (title, created_at) VALUES (?, ?)",
			fmt.Sprintf("evt %d", i),
			fmt.Sprintf("2026-01-0%d 10:00:00", i+1),
		)
		if err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
	}
	for i := range 5 {
		if _, err := database.Exec("INSERT INTO events_fts (rowid, content) VALUES (?, ?)", i+1, fmt.Sprintf("evt %d", i)); err != nil {
			t.Fatalf("seed FTS %d: %v", i, err)
		}
	}

	desc, err := List(ctx, database, ListOpts{Limit: 2})
	if err != nil {
		t.Fatalf("List default limit: %v", err)
	}
	if len(desc) != 2 || desc[0].Title != "evt 4" {
		t.Errorf("default limit got %d events, first=%q; want 2 starting with 'evt 4'", len(desc), desc[0].Title)
	}

	// Ascending decides the display order, never which rows survive the
	// limit: both of these are the newest two events, oldest-first. Applying
	// ORDER BY before LIMIT in one statement used to return evt 0 and evt 1.
	asc, err := List(ctx, database, ListOpts{Limit: 2, Ascending: true})
	if err != nil {
		t.Fatalf("List ascending limit: %v", err)
	}
	if len(asc) != 2 || asc[0].Title != "evt 3" || asc[1].Title != "evt 4" {
		t.Errorf("ascending limit got %v; want [evt 3, evt 4]", titlesOf(asc))
	}
}

// TestList_LimitAndSortWithFilter pins that the newest-N subquery composes with
// a compiled -S filter rather than losing its WHERE clause to the wrapper, and
// that ListSeq — which shares buildListQuery — gets the same rows as List.
func TestList_LimitAndSortWithFilter(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	// keep 0, skip 1, keep 2, skip 3, keep 4 — oldest to newest.
	for i := range 5 {
		title := fmt.Sprintf("keep %d", i)
		if i%2 == 1 {
			title = fmt.Sprintf("skip %d", i)
		}
		if _, err := database.Exec(
			"INSERT INTO events (title, created_at) VALUES (?, ?)",
			title, fmt.Sprintf("2026-01-0%d 10:00:00", i+1),
		); err != nil {
			t.Fatalf("seed event %d: %v", i, err)
		}
		if _, err := database.Exec("INSERT INTO events_fts (rowid, content) VALUES (?, ?)", i+1, title); err != nil {
			t.Fatalf("seed FTS %d: %v", i, err)
		}
	}

	opts := ListOpts{Filter: "keep", Limit: 2, Ascending: true}
	want := []string{"keep 2", "keep 4"}

	got, err := List(ctx, database, opts)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !slices.Equal(titlesOf(got), want) {
		t.Errorf("List got %v, want %v", titlesOf(got), want)
	}

	var streamed []Event
	for ev, err := range ListSeq(ctx, database, opts) {
		if err != nil {
			t.Fatalf("ListSeq: %v", err)
		}
		streamed = append(streamed, ev)
	}
	if !slices.Equal(titlesOf(streamed), want) {
		t.Errorf("ListSeq got %v, want %v", titlesOf(streamed), want)
	}
}

// titlesOf renders an event slice for failure messages.
func titlesOf(events []Event) []string {
	out := make([]string, len(events))
	for i, ev := range events {
		out[i] = ev.Title
	}
	return out
}

// idsOf is titlesOf for the tests that compare topology or filter results.
func idsOf(events []Event) []int64 {
	out := make([]int64, len(events))
	for i, ev := range events {
		out[i] = ev.ID
	}
	return out
}

func TestList_LoadMetaAcrossChunkBoundary(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	const n = metaBatchSize + 50
	for i := range n {
		if _, err := Add(ctx, database, AddInput{Title: "evt", Meta: []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	events, err := List(ctx, database, ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != n {
		t.Fatalf("len(events) = %d, want %d", len(events), n)
	}
	for i, ev := range events {
		if len(ev.Meta) != 1 || ev.Meta[0].Key != MetaKeyAuthor || ev.Meta[0].Value != "alice" {
			t.Fatalf("events[%d].Meta = %v, want [{author alice}]", i, ev.Meta)
		}
	}
}

func TestListSeq_YieldsAllInOrder(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	for i := range 3 {
		if _, err := Add(ctx, database, AddInput{Title: fmt.Sprintf("evt %d", i), Meta: []parse.Meta{
			{Key: MetaKeyAuthor, Value: "alice"},
		}}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	var got []string
	for ev, err := range ListSeq(ctx, database, ListOpts{Ascending: true}) {
		if err != nil {
			t.Fatalf("ListSeq: %v", err)
		}
		if len(ev.Meta) != 1 || ev.Meta[0].Value != "alice" {
			t.Errorf("event %d meta = %v, want [{author alice}]", ev.ID, ev.Meta)
		}
		got = append(got, ev.Title)
	}
	want := []string{"evt 0", "evt 1", "evt 2"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestListSeq_AcrossBatchBoundary(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	const n = metaBatchSize + 50
	for i := range n {
		if _, err := Add(ctx, database, AddInput{Title: fmt.Sprintf("e%d", i), Meta: []parse.Meta{
			{Key: MetaKeyAuthor, Value: "alice"},
		}}); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	count := 0
	for ev, err := range ListSeq(ctx, database, ListOpts{Ascending: true}) {
		if err != nil {
			t.Fatalf("ListSeq err: %v", err)
		}
		if len(ev.Meta) != 1 {
			t.Fatalf("event %d meta missing across batch boundary", ev.ID)
		}
		count++
	}
	if count != n {
		t.Errorf("yielded %d events, want %d", count, n)
	}
}

func TestReparent_DetachClearsParent(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	parent, err := Add(ctx, database, AddInput{Title: "parent"})
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	child, err := Add(ctx, database, AddInput{Title: "child", ParentID: &parent})
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	if err := Reparent(ctx, database, child, nil); err != nil {
		t.Fatalf("Reparent detach: %v", err)
	}

	ev, err := Get(ctx, database, child)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.ParentID != nil {
		t.Errorf("ParentID = %d, want nil", *ev.ParentID)
	}
}

func TestReparent_AllowsValidMove(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	a, _ := Add(ctx, database, AddInput{Title: "a"})
	b, _ := Add(ctx, database, AddInput{Title: "b"})
	c, _ := Add(ctx, database, AddInput{Title: "c", ParentID: &a})

	if err := Reparent(ctx, database, c, &b); err != nil {
		t.Fatalf("Reparent: %v", err)
	}
	ev, _ := Get(ctx, database, c)
	if ev.ParentID == nil || *ev.ParentID != b {
		t.Errorf("ParentID = %v, want %d", ev.ParentID, b)
	}
}

func TestReparent_RejectsSelf(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, AddInput{Title: "x"})

	err := Reparent(ctx, database, id, &id)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
	assertNoDoubledCycleText(t, err)
	if want := fmt.Sprintf("attaching event %d to itself", id); !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err, want)
	}
}

func TestReparent_RejectsAncestryCycle(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	// 1 -> 2 -> 3   (3 has parent 2 has parent 1)
	a, _ := Add(ctx, database, AddInput{Title: "a"})
	b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
	c, _ := Add(ctx, database, AddInput{Title: "c", ParentID: &b})

	// Attaching a (top) to c (descendant) would form a cycle.
	err := Reparent(ctx, database, a, &c)
	if !errors.Is(err, ErrCycle) {
		t.Fatalf("err = %v, want ErrCycle", err)
	}
	assertNoDoubledCycleText(t, err)
	if want := fmt.Sprintf("attaching event %d to event %d", a, c); !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err, want)
	}
}

// assertNoDoubledCycleText checks that the prefix does not restate what the
// sentinel already says. Both messages used to read "... would form a cycle:
// would create a parent cycle"; the prefix's job is the ids.
func assertNoDoubledCycleText(t *testing.T, err error) {
	t.Helper()
	if n := strings.Count(err.Error(), "cycle"); n != 1 {
		t.Errorf("err = %q says \"cycle\" %d times, want exactly 1", err, n)
	}
}

// forgeParent wires parent_id directly, bypassing Reparent's guard, to build
// the corrupt tree fngr cannot write but can be handed: a `.fngr.db` picked up
// from an untrusted directory, a hand-edit, a half-written file.
func forgeParent(t *testing.T, database *sql.DB, child, parent int64) {
	t.Helper()
	if _, err := database.Exec("UPDATE events SET parent_id = ? WHERE id = ?", parent, child); err != nil {
		t.Fatalf("forge parent of %d as %d: %v", child, parent, err)
	}
}

// boundedCtx caps a traversal that is supposed to terminate on its own. If a
// regression brings the unbounded loop back, the query fails on the deadline
// and the test reports the wrong error in seconds instead of hanging until
// the whole package times out.
func boundedCtx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	t.Cleanup(cancel)
	return c
}

// TestReparent_CorruptAncestryTerminates is the M2 walk bound. In both cases
// the cycle sits upstream of the event being moved rather than containing it,
// so the `parent == id` check never fires and the walk climbed forever at full
// CPU with the transaction still open.
func TestReparent_CorruptAncestryTerminates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		forge func(t *testing.T, database *sql.DB, a, b int64)
	}{
		{
			name:  "two events pointing at each other",
			forge: func(t *testing.T, database *sql.DB, a, b int64) { forgeParent(t, database, a, b) },
		},
		{
			// The one-event cycle's first hop is already a repeat, so the
			// seen set has to be seeded with the starting node rather than
			// filled only as the walk moves.
			name:  "self-parent",
			forge: func(t *testing.T, database *sql.DB, a, _ int64) { forgeParent(t, database, a, a) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)

			a, _ := Add(ctx, database, AddInput{Title: "a"})
			b, _ := Add(ctx, database, AddInput{Title: "b", ParentID: &a})
			outsider, _ := Add(ctx, database, AddInput{Title: "outsider"})
			tt.forge(t, database, a, b)

			err := Reparent(boundedCtx(t), database, outsider, &a)
			if !errors.Is(err, ErrCorruptTree) {
				t.Errorf("err = %v, want ErrCorruptTree", err)
			}
		})
	}
}

func TestReparent_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	err := Reparent(ctx, database, 9999, nil)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestReparent_NewParentNotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, _ := Add(ctx, database, AddInput{Title: "x"})

	missing := int64(9999)
	err := Reparent(ctx, database, id, &missing)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAddTags_DedupsAtDB(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
	}})

	tags := []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "tag", Value: "deploy"},
		{Key: "people", Value: "alice"},
	}
	added, err := AddTags(ctx, database, id, tags)
	if err != nil {
		t.Fatalf("AddTags: %v", err)
	}
	if added != 2 {
		t.Errorf("added = %d, want 2 (one of three tags was already present)", added)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := map[parse.Meta]bool{
		{Key: "tag", Value: "ops"}:      true,
		{Key: "tag", Value: "deploy"}:   true,
		{Key: "people", Value: "alice"}: true,
	}
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta entries, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v", m)
		}
	}
}

func TestAddTags_AddedCount(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, _ := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{{Key: "tag", Value: "ops"}}})

	cases := []struct {
		name string
		tags []parse.Meta
		want int64
	}{
		{
			name: "all-new",
			tags: []parse.Meta{{Key: "tag", Value: "deploy"}, {Key: "people", Value: "alice"}},
			want: 2,
		},
		{
			name: "all-dupes",
			tags: []parse.Meta{{Key: "tag", Value: "ops"}, {Key: "tag", Value: "deploy"}, {Key: "people", Value: "alice"}},
			want: 0,
		},
		{
			name: "single-new",
			tags: []parse.Meta{{Key: "env", Value: "prod"}},
			want: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			added, err := AddTags(ctx, database, id, tc.tags)
			if err != nil {
				t.Fatalf("AddTags: %v", err)
			}
			if added != tc.want {
				t.Errorf("added = %d, want %d", added, tc.want)
			}
		})
	}
}

func TestAddTags_RebuildsFTS(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, AddInput{Title: "no tags here"})
	if _, err := AddTags(ctx, database, id, []parse.Meta{{Key: "tag", Value: "ops"}}); err != nil {
		t.Fatalf("AddTags: %v", err)
	}

	matches, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("FTS not refreshed: %d matches for #ops, want 1", len(matches))
	}
}

func TestAddTags_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	_, err := AddTags(ctx, database, 9999, []parse.Meta{{Key: "tag", Value: "x"}})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestUnstorableMetaRefusedAtTheWriter pins the half of the rule that cannot
// be bypassed. parse.ValidateMeta guards CLI arguments, but parse.Meta is an
// exported struct with exported fields, so a directly-built AddInput — a
// library caller, a future importer, a test — reaches the INSERT with no
// argument parser in the way. Every write path has to refuse the tuple, or
// the rows nothing can search for are still creatable.
func TestUnstorableMetaRefusedAtTheWriter(t *testing.T) {
	t.Parallel()

	bad := []struct {
		name string
		meta parse.Meta
	}{
		{"empty value", parse.Meta{Key: "k", Value: ""}},
		{"whitespace key", parse.Meta{Key: "a b", Value: "c"}},
		{"empty key", parse.Meta{Key: "", Value: "v"}},
	}
	for _, tt := range bad {
		t.Run("Add/"+tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			if _, err := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{tt.meta}}); err == nil {
				t.Fatalf("Add accepted %+v", tt.meta)
			}
		})
		t.Run("AddMany/"+tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			in := []AddInput{{Title: "ok"}, {Title: "x", Meta: []parse.Meta{tt.meta}}}
			if _, err := AddMany(ctx, database, in); err == nil {
				t.Fatalf("AddMany accepted %+v", tt.meta)
			}
			// Atomic: the good record must not survive the bad one.
			got, err := List(ctx, database, ListOpts{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("AddMany left %d events behind, want 0", len(got))
			}
		})
		t.Run("AddTags/"+tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			id, err := Add(ctx, database, AddInput{Title: "x"})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			if _, err := AddTags(ctx, database, id, []parse.Meta{tt.meta}); err == nil {
				t.Fatalf("AddTags accepted %+v", tt.meta)
			}
		})
		t.Run("UpdateMeta/"+tt.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)
			if _, err := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{{Key: "tag", Value: "wip"}}}); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if _, err := UpdateMeta(ctx, database, "tag", "wip", tt.meta.Key, tt.meta.Value); err == nil {
				t.Fatalf("UpdateMeta renamed onto %+v", tt.meta)
			}
		})
	}
}

// TestRemoveTagsNamesWhatMintingRefuses is the other half: the verbs that name
// an existing row stay permissive, or the tuples older builds wrote become the
// tuples nothing can take out. The row is planted through the meta table
// directly, since no current write path will produce it.
func TestRemoveTagsNamesWhatMintingRefuses(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "x"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := database.ExecContext(ctx,
		"INSERT INTO event_meta (event_id, key, value, source) VALUES (?, 'k', '', 'explicit')", id); err != nil {
		t.Fatalf("plant legacy row: %v", err)
	}

	n, err := RemoveTags(ctx, database, id, []parse.Meta{{Key: "k", Value: ""}})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if n != 1 {
		t.Errorf("removed %d rows, want 1", n)
	}
}

func TestAddTags_EmptyIsNoOp(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, _ := Add(ctx, database, AddInput{Title: "x"})
	added, err := AddTags(ctx, database, id, nil)
	if err != nil {
		t.Errorf("empty AddTags returned err: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0 for empty tags", added)
	}
}

func TestRemoveTags_ReturnsCount(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "tag", Value: "deploy"},
		{Key: "people", Value: "alice"},
	}})

	n, err := RemoveTags(ctx, database, id, []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "tag", Value: "missing"},
	})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}

	ev, _ := Get(ctx, database, id)
	for _, m := range ev.Meta {
		if m.Key == "tag" && m.Value == "ops" {
			t.Error("tag=ops not removed")
		}
	}
}

func TestRemoveTags_RebuildsFTS(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, AddInput{Title: "x #ops", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
	}})

	if _, err := RemoveTags(ctx, database, id, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}); err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}

	matches, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("FTS not refreshed: %d matches for #ops, want 0", len(matches))
	}
}

func TestRemoveTags_NoMatchReturnsZero(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, AddInput{Title: "x"})
	n, err := RemoveTags(ctx, database, id, []parse.Meta{
		{Key: "tag", Value: "ghost"},
	})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if n != 0 {
		t.Errorf("removed = %d, want 0", n)
	}
}

func TestRemoveTags_NotFound(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	_, err := RemoveTags(ctx, database, 9999, []parse.Meta{{Key: "tag", Value: "x"}})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRemoveTags_EmptyIsNoOp(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, _ := Add(ctx, database, AddInput{Title: "x"})
	n, err := RemoveTags(ctx, database, id, nil)
	if err != nil {
		t.Errorf("empty RemoveTags err: %v", err)
	}
	if n != 0 {
		t.Errorf("empty RemoveTags returned %d, want 0", n)
	}
}

func TestUpdate_TextSyncsBodyTags(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	// Pre-existing meta: body-derived (alice, ops) plus non-body (env=prod, author).
	id, err := Add(ctx, database, AddInput{Title: "deploy with @alice #ops", Meta: []parse.Meta{
		{Key: "author", Value: "nicolas"},
		{Key: "people", Value: "alice"},
		{Key: "tag", Value: "ops"},
		{Key: "env", Value: "prod"},
	}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Replace text: drop @alice, add @bob, keep #ops.
	newText := "rolled back with @bob #ops"
	if err := Update(ctx, database, id, &newText, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := map[parse.Meta]bool{
		{Key: "author", Value: "nicolas"}: true, // non-body — preserved
		{Key: "env", Value: "prod"}:       true, // non-body — preserved
		{Key: "tag", Value: "ops"}:        true, // body in both → kept (exactly once)
		{Key: "people", Value: "bob"}:     true, // new body tag
	}
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta entries, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v (alice should have been removed)", m)
		}
	}
}

func TestUpdate_TextDedupsRepeatedBodyTags(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, _ := Add(ctx, database, AddInput{Title: "x #ops", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
	}})

	newText := "y #ops #ops"
	if err := Update(ctx, database, id, &newText, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	n, err := CountMeta(ctx, database, "tag", "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if n != 1 {
		t.Errorf("tag=ops count = %d, want 1", n)
	}
}

// TestUpdate_RetractsOnlyBodyDerivedTags is the M1 guard. `people=bob` from
// an inline `@bob` and `people=bob` from `event tag` are the same row, so
// before event_meta carried provenance the sync — delete every tag in the old
// text, insert every tag in the new — destroyed the operator's tag the moment
// an edit dropped a mention of the same handle. Untrusted notes contain
// `@handle` all the time, so this needed no unusual usage to hit.
//
// Each case ends with the mention edited away; only the tag nothing but the
// body ever claimed should go with it.
func TestUpdate_RetractsOnlyBodyDerivedTags(t *testing.T) {
	t.Parallel()

	bob := parse.Meta{Key: MetaKeyPeople, Value: "bob"}
	tagIt := func(t *testing.T, database *sql.DB, id int64) {
		if _, err := AddTags(ctx, database, id, []parse.Meta{bob}); err != nil {
			t.Fatalf("AddTags: %v", err)
		}
	}
	cases := []struct {
		name string
		// claim asserts the tag as the operator's — or, for the baseline,
		// does not. It runs between the two edits unless claimFirst.
		claim func(t *testing.T, database *sql.DB, id int64)
		// claimFirst runs claim before the mention is ever added, which is
		// the order the bug was reported in: the tag row then predates any
		// body that yields it, so the mention's insert is the conflicting one.
		claimFirst bool
		wantKept   bool
	}{
		{
			name:     "body only",
			claim:    func(*testing.T, *sql.DB, int64) {},
			wantKept: false,
		},
		{
			name:     "event tag",
			claim:    tagIt,
			wantKept: true,
		},
		{
			name:       "event tag before the mention",
			claim:      tagIt,
			claimFirst: true,
			wantKept:   true,
		},
		{
			name: "meta rename onto it",
			claim: func(t *testing.T, database *sql.DB, id int64) {
				if _, err := UpdateMeta(ctx, database, MetaKeyPeople, "bob", MetaKeyPeople, "bob"); err != nil {
					t.Fatalf("UpdateMeta: %v", err)
				}
			},
			wantKept: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			database := testDB(t)

			id, err := Add(ctx, database, AddInput{Title: "plain note", Meta: []parse.Meta{
				{Key: MetaKeyAuthor, Value: "nicolas"},
			}})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}

			if tc.claimFirst {
				tc.claim(t, database, id)
			}
			mention := "now mentions @bob inline"
			if err := Update(ctx, database, id, nil, &mention, nil); err != nil {
				t.Fatalf("Update(%q): %v", mention, err)
			}
			if !tc.claimFirst {
				tc.claim(t, database, id)
			}
			gone := "no more mention"
			if err := Update(ctx, database, id, nil, &gone, nil); err != nil {
				t.Fatalf("Update(%q): %v", gone, err)
			}

			ev, err := Get(ctx, database, id)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got := slices.Contains(ev.Meta, bob); got != tc.wantKept {
				t.Errorf("people=bob present = %v, want %v; meta = %v", got, tc.wantKept, ev.Meta)
			}
		})
	}
}

// TestAddTags_CountsPromotionAsAlreadyPresent pins the reporting side of the
// promotion in AddTags. Tagging a handle the body already mentions rewrites
// the row's provenance, which RowsAffected counts as a change — so the count
// has to come from the pre-read instead, or `fngr event tag` claims to have
// added a tag that was on screen before the command ran.
func TestAddTags_CountsPromotionAsAlreadyPresent(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "standup with @bob", Meta: []parse.Meta{
		{Key: MetaKeyAuthor, Value: "nicolas"},
		{Key: MetaKeyPeople, Value: "bob"},
	}})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	added, err := AddTags(ctx, database, id, []parse.Meta{
		{Key: MetaKeyPeople, Value: "bob"},
		{Key: MetaKeyTag, Value: "standup"},
	})
	if err != nil {
		t.Fatalf("AddTags: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (people=bob was already there)", added)
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

// TestUpdateMeta_AuthorValueIsCorrectable is the other half of the protected
// key rule. `author` is refused as the target of every meta verb so no event
// can end up with zero or two of them — but a rename that keeps the key only
// rewrites the value, so every event keeps exactly the one row it had. Without
// the exception a typo in --author was permanent: no verb could touch it.
func TestUpdateMeta_AuthorValueIsCorrectable(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	for _, title := range []string{"first", "second"} {
		if _, err := Add(ctx, database, AddInput{Title: title, Meta: []parse.Meta{
			{Key: MetaKeyAuthor, Value: "nicolass"},
		}}); err != nil {
			t.Fatalf("Add(%q): %v", title, err)
		}
	}

	n, err := UpdateMeta(ctx, database, MetaKeyAuthor, "nicolass", MetaKeyAuthor, "nicolas")
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	if n != 2 {
		t.Errorf("renamed %d rows, want 2", n)
	}

	ev, err := Get(ctx, database, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := AuthorOf(ev.Meta); got != "nicolas" {
		t.Errorf("author = %q, want %q; meta = %v", got, "nicolas", ev.Meta)
	}

	// The escape hatch is only for the value. Renaming author to an empty
	// value, or across keys in either direction, still breaks the count.
	blocked := []struct{ oldKey, oldValue, newKey, newValue string }{
		{MetaKeyAuthor, "nicolas", MetaKeyAuthor, ""},
		{MetaKeyAuthor, "nicolas", MetaKeyPeople, "nicolas"},
		{MetaKeyPeople, "bob", MetaKeyAuthor, "bob"},
	}
	for _, b := range blocked {
		if _, err := UpdateMeta(ctx, database, b.oldKey, b.oldValue, b.newKey, b.newValue); err == nil {
			t.Errorf("UpdateMeta(%s=%s -> %s=%s) succeeded, want refusal",
				b.oldKey, b.oldValue, b.newKey, b.newValue)
		}
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

func TestUpdate_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, err := Add(context.Background(), database, AddInput{Title: "starter"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	empty := ""
	if err := Update(context.Background(), database, id, &empty, nil, nil); err == nil {
		t.Error("Update with empty title: expected error")
	}
}

func TestUpdate_BodyOnly(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, err := Add(context.Background(), database, AddInput{Title: "title", Body: "body"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	newBody := "newbody"
	if err := Update(context.Background(), database, id, nil, &newBody, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != "title" || ev.Body != "newbody" {
		t.Errorf("got (title=%q, body=%q), want (title, newbody)", ev.Title, ev.Body)
	}
}

func TestUpdate_TitleOnly(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, err := Add(context.Background(), database, AddInput{Title: "title", Body: "body"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	newTitle := "newtitle"
	if err := Update(context.Background(), database, id, &newTitle, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != "newtitle" || ev.Body != "body" {
		t.Errorf("got (title=%q, body=%q), want (newtitle, body)", ev.Title, ev.Body)
	}
}

func TestUpdate_ClearsBody(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, err := Add(context.Background(), database, AddInput{Title: "title", Body: "body"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	empty := ""
	if err := Update(context.Background(), database, id, nil, &empty, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != "title" || ev.Body != "" {
		t.Errorf("got (title=%q, body=%q), want (title, '')", ev.Title, ev.Body)
	}
}

func TestUpdate_BodyTagSyncAcrossFields(t *testing.T) {
	t.Parallel()
	database := testDB(t)
	id, err := Add(context.Background(), database, AddInput{
		Title: "deploy #ops",
		Body:  "@sarah",
		Meta: []parse.Meta{
			{Key: "tag", Value: "ops"},
			{Key: "people", Value: "sarah"},
		},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Move tags from title→body and body→title; expected meta set is unchanged.
	newTitle := "@sarah deploy"
	newBody := "#ops"
	if err := Update(context.Background(), database, id, &newTitle, &newBody, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	ev, err := Get(context.Background(), database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	wantSet := map[parse.Meta]bool{
		{Key: "tag", Value: "ops"}:      true,
		{Key: "people", Value: "sarah"}: true,
	}
	for _, m := range ev.Meta {
		if wantSet[m] {
			delete(wantSet, m)
		}
	}
	if len(wantSet) != 0 {
		t.Errorf("missing meta after move: %v", wantSet)
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

// TestUpdate_RejectsOutOfRangeTimestamp mirrors the Add guard on the edit path.
func TestUpdate_RejectsOutOfRangeTimestamp(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	id, err := Add(ctx, database, AddInput{Title: "ok"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	bad := time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := Update(ctx, database, id, nil, nil, &bad); !errors.Is(err, ErrTimeRange) {
		t.Fatalf("Update: err = %v, want ErrTimeRange", err)
	}

	// The rejected write must not have partially landed.
	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !timefmt.InRange(ev.CreatedAt) {
		t.Errorf("CreatedAt = %v, want the original in-range timestamp", ev.CreatedAt)
	}
}

// TestScan_PoisonedTimestampStaysUsable covers a database already damaged by
// an older build. A created_at the driver cannot convert used to abort the
// whole result set, so list, show and even delete failed — delete calls Get
// first, leaving no CLI path to remove the row. The row must now read back
// with a zero CreatedAt and stay deletable, and its neighbours must survive.
func TestScan_PoisonedTimestampStaysUsable(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	if _, err := Add(ctx, database, AddInput{Title: "healthy before"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	res, err := database.ExecContext(ctx,
		`INSERT INTO events (parent_id, title, body, created_at) VALUES (NULL, ?, '', ?)`,
		"poisoned", "-0712-08-29 14:07:26")
	if err != nil {
		t.Fatalf("insert poisoned row: %v", err)
	}
	badID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	if _, err := Add(ctx, database, AddInput{Title: "healthy after"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	events, err := List(ctx, database, ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("List returned %d events, want 3", len(events))
	}

	// Streaming path too — it scans rows independently of List.
	var streamed int
	for _, err := range ListSeq(ctx, database, ListOpts{}) {
		if err != nil {
			t.Fatalf("ListSeq: %v", err)
		}
		streamed++
	}
	if streamed != 3 {
		t.Errorf("ListSeq yielded %d events, want 3", streamed)
	}

	ev, err := Get(ctx, database, badID)
	if err != nil {
		t.Fatalf("Get(poisoned): %v", err)
	}
	if ev.Title != "poisoned" {
		t.Errorf("Title = %q, want %q", ev.Title, "poisoned")
	}
	if !ev.CreatedAt.IsZero() {
		t.Errorf("CreatedAt = %v, want the zero time for an unreadable stamp", ev.CreatedAt)
	}

	if err := Delete(ctx, database, badID); err != nil {
		t.Fatalf("Delete(poisoned): %v", err)
	}
	remaining, err := List(ctx, database, ListOpts{})
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("after delete: %d events, want 2", len(remaining))
	}
}

// TestTimeFromDriverValue covers the conversion directly. The string branch is
// live — it is what a poisoned row takes — but its *successful* parse, and the
// []byte branch, are unreachable through the current driver, which converts
// the canonical layout itself. They exist so a driver upgrade that stops
// auto-converting degrades instead of zeroing every timestamp.
func TestTimeFromDriverValue(t *testing.T) {
	t.Parallel()
	want := time.Date(2026, 7, 27, 14, 7, 26, 0, time.UTC)
	tests := []struct {
		name string
		in   any
		want time.Time
	}{
		{"time.Time passes through", want, want},
		{"canonical string", "2026-07-27 14:07:26", want},
		{"canonical bytes", []byte("2026-07-27 14:07:26"), want},
		{"unparseable string", "-0712-08-29 14:07:26", time.Time{}},
		{"unparseable bytes", []byte("not a time"), time.Time{}},
		{"nil", nil, time.Time{}},
		{"unexpected type", int64(42), time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := timeFromDriverValue(tt.in); !got.Equal(tt.want) {
				t.Errorf("timeFromDriverValue(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}
