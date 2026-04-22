package event

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/parse"
)

var ctx = context.Background()

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(":memory:", true)
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

	id, err := Add(ctx, database, "standup with @bob #meeting", nil, meta, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id < 1 {
		t.Fatalf("expected positive event ID, got %d", id)
	}

	var text string
	err = database.QueryRow("SELECT text FROM events WHERE id = ?", id).Scan(&text)
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

	parentID, err := Add(ctx, database, "parent event", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}

	childID, err := Add(ctx, database, "child event", &parentID, nil, nil)
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
	_, err := Add(ctx, database, "orphan event", &invalidParent, nil, nil)
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

	id, err := Add(ctx, database, "get me", nil, meta, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	ev, err := Get(ctx, database, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if ev.Text != "get me" {
		t.Errorf("event.Text = %q, want %q", ev.Text, "get me")
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

	id, err := Add(ctx, database, "to be deleted", nil, nil, nil)
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

func TestDelete_CascadesChildren(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	parentID, err := Add(ctx, database, "parent", nil, []parse.Meta{{Key: "author", Value: "alice"}}, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}

	childID, err := Add(ctx, database, "child", &parentID, []parse.Meta{{Key: "tag", Value: "reply"}}, nil)
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

	_, err := Add(ctx, database, "first event #work", nil, []parse.Meta{{Key: "tag", Value: "work"}}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	_, err = Add(ctx, database, "second event #personal", nil, []parse.Meta{{Key: "tag", Value: "personal"}}, nil)
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
	if events[0].Text != "first event #work" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "first event #work")
	}
	if events[1].Text != "second event #personal" {
		t.Errorf("events[1].Text = %q, want %q", events[1].Text, "second event #personal")
	}
}

func TestList_WithFilter(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "deploy to prod #ops", nil, []parse.Meta{{Key: "tag", Value: "ops"}}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}
	_, err = Add(ctx, database, "standup meeting #work", nil, []parse.Meta{{Key: "tag", Value: "work"}}, nil)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	events, err := List(ctx, database, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "deploy to prod #ops" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "deploy to prod #ops")
	}
}

func TestList_WithDateRange(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := database.Exec("INSERT INTO events (text, created_at) VALUES (?, ?)", "old event", "2026-01-01 00:00:00")
	if err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	_, err = database.Exec("INSERT INTO events_fts (rowid, content) VALUES (1, 'old event')")
	if err != nil {
		t.Fatalf("insert old FTS: %v", err)
	}

	_, err = database.Exec("INSERT INTO events (text, created_at) VALUES (?, ?)", "new event", "2026-03-15 12:00:00")
	if err != nil {
		t.Fatalf("insert new event: %v", err)
	}
	_, err = database.Exec("INSERT INTO events_fts (rowid, content) VALUES (2, 'new event')")
	if err != nil {
		t.Fatalf("insert new FTS: %v", err)
	}

	events, err := List(ctx, database, ListOpts{From: "2026-03-01"})
	if err != nil {
		t.Fatalf("List with From: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "new event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "new event")
	}

	events, err = List(ctx, database, ListOpts{To: "2026-02-01"})
	if err != nil {
		t.Fatalf("List with To: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "old event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "old event")
	}

	events, err = List(ctx, database, ListOpts{From: "2026-02-01", To: "2026-04-01"})
	if err != nil {
		t.Fatalf("List with From and To: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "new event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "new event")
	}
}

func TestListMeta(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "event one", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	_, err = Add(ctx, database, "event two", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "personal"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	counts, err := ListMeta(ctx, database)
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

func TestGetSubtree(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	root, err := Add(ctx, database, "root", nil, []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}, nil)
	if err != nil {
		t.Fatalf("Add root: %v", err)
	}

	child, err := Add(ctx, database, "child", &root, []parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}, nil)
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	grandchild, err := Add(ctx, database, "grandchild", &child, []parse.Meta{{Key: MetaKeyAuthor, Value: "bob"}}, nil)
	if err != nil {
		t.Fatalf("Add grandchild: %v", err)
	}

	if _, err := Add(ctx, database, "unrelated", nil, nil, nil); err != nil {
		t.Fatalf("Add unrelated: %v", err)
	}

	events, err := GetSubtree(ctx, database, root)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3", len(events))
	}

	ids := make([]int64, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}

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

	id, err := Add(ctx, database, "leaf", nil, nil, nil)
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
	if events[0].Text != "leaf" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "leaf")
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

func TestFTSIsolation_MetaTokensNotMatchedByBareWords(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "pushed to production", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "deploy"},
	}, nil)
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

	_, err := Add(ctx, database, "heading to work early", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
	}, nil)
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

func TestList_ComplexFilters(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := Add(ctx, database, "deploy to prod", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "ops"},
		{Key: MetaKeyPeople, Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 1: %v", err)
	}

	_, err = Add(ctx, database, "standup meeting", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "bob"},
		{Key: MetaKeyTag, Value: "work"},
		{Key: MetaKeyPeople, Value: "bob"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 2: %v", err)
	}

	_, err = Add(ctx, database, "deploy standup", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "ops"},
		{Key: MetaKeyTag, Value: "work"},
		{Key: MetaKeyPeople, Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add 3: %v", err)
	}

	tests := []struct {
		name   string
		filter string
		want   int
	}{
		{"AND tags", "#ops & #work", 1},
		{"OR tags", "#ops | #work", 3},
		{"NOT tag", "!#work", 1},
		{"tag AND person", "#ops & @alice", 2},
		{"body AND tag", "deploy & #ops", 2},
		{"body NOT tag", "deploy & !#work", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, err := List(ctx, database, ListOpts{Filter: tt.filter})
			if err != nil {
				t.Fatalf("List(%q): %v", tt.filter, err)
			}
			if len(events) != tt.want {
				texts := make([]string, len(events))
				for i, e := range events {
					texts[i] = e.Text
				}
				t.Errorf("filter %q matched %d events %v, want %d", tt.filter, len(events), texts, tt.want)
			}
		})
	}
}
