package internal

import (
	"strings"
	"testing"
)

func TestAddEvent(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	meta := []Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "meeting"},
		{Key: "people", Value: "bob"},
	}

	id, err := AddEvent(db, "standup with @bob #meeting", nil, meta)
	if err != nil {
		t.Fatalf("AddEvent: %v", err)
	}
	if id < 1 {
		t.Fatalf("expected positive event ID, got %d", id)
	}

	// Verify event row exists.
	var text string
	err = db.QueryRow("SELECT text FROM events WHERE id = ?", id).Scan(&text)
	if err != nil {
		t.Fatalf("query event row: %v", err)
	}
	if text != "standup with @bob #meeting" {
		t.Errorf("event text = %q, want %q", text, "standup with @bob #meeting")
	}

	// Verify 3 metadata rows.
	var metaCount int
	err = db.QueryRow("SELECT COUNT(*) FROM event_meta WHERE event_id = ?", id).Scan(&metaCount)
	if err != nil {
		t.Fatalf("count meta: %v", err)
	}
	if metaCount != 3 {
		t.Errorf("meta count = %d, want 3", metaCount)
	}

	// Verify FTS match.
	var ftsCount int
	err = db.QueryRow("SELECT COUNT(*) FROM events_fts WHERE events_fts MATCH ?", "standup").Scan(&ftsCount)
	if err != nil {
		t.Fatalf("FTS query: %v", err)
	}
	if ftsCount != 1 {
		t.Errorf("FTS match count = %d, want 1", ftsCount)
	}
}

func TestAddEvent_WithParent(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	parentID, err := AddEvent(db, "parent event", nil, nil)
	if err != nil {
		t.Fatalf("AddEvent parent: %v", err)
	}

	childID, err := AddEvent(db, "child event", &parentID, nil)
	if err != nil {
		t.Fatalf("AddEvent child: %v", err)
	}

	// Verify parent_id FK is set.
	var storedParentID int64
	err = db.QueryRow("SELECT parent_id FROM events WHERE id = ?", childID).Scan(&storedParentID)
	if err != nil {
		t.Fatalf("query child parent_id: %v", err)
	}
	if storedParentID != parentID {
		t.Errorf("parent_id = %d, want %d", storedParentID, parentID)
	}
}

func TestAddEvent_InvalidParent(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	invalidParent := int64(9999)
	_, err := AddEvent(db, "orphan event", &invalidParent, nil)
	if err == nil {
		t.Fatal("expected error for invalid parent, got nil")
	}
	if !strings.Contains(err.Error(), "parent event 9999 not found") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "parent event 9999 not found")
	}
}

func TestGetEvent(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	meta := []Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}

	id, err := AddEvent(db, "get me", nil, meta)
	if err != nil {
		t.Fatalf("AddEvent: %v", err)
	}

	event, err := GetEvent(db, id)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}

	if event.Text != "get me" {
		t.Errorf("event.Text = %q, want %q", event.Text, "get me")
	}
	if len(event.Meta) != 2 {
		t.Errorf("len(event.Meta) = %d, want 2", len(event.Meta))
	}
}

func TestGetEvent_NotFound(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	_, err := GetEvent(db, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if !strings.Contains(err.Error(), "event 9999 not found") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "event 9999 not found")
	}
}

func TestDeleteEvent(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	id, err := AddEvent(db, "to be deleted", nil, nil)
	if err != nil {
		t.Fatalf("AddEvent: %v", err)
	}

	if err := DeleteEvent(db, id); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}

	// Verify event is gone.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", id).Scan(&count)
	if err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected event to be deleted, got count=%d", count)
	}
}

func TestDeleteEvent_NotFound(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	err := DeleteEvent(db, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
}

func TestDeleteEvent_CascadesChildren(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	parentID, err := AddEvent(db, "parent", nil, []Meta{{Key: "author", Value: "alice"}})
	if err != nil {
		t.Fatalf("AddEvent parent: %v", err)
	}

	childID, err := AddEvent(db, "child", &parentID, []Meta{{Key: "tag", Value: "reply"}})
	if err != nil {
		t.Fatalf("AddEvent child: %v", err)
	}

	if err := DeleteEvent(db, parentID); err != nil {
		t.Fatalf("DeleteEvent parent: %v", err)
	}

	// Verify child is also gone.
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM events WHERE id = ?", childID).Scan(&count)
	if err != nil {
		t.Fatalf("count child: %v", err)
	}
	if count != 0 {
		t.Errorf("expected child event to be cascade-deleted, got count=%d", count)
	}
}

func TestListMeta(t *testing.T) {
	db := testDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	// Add 2 events with overlapping meta.
	_, err := AddEvent(db, "event one", nil, []Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	})
	if err != nil {
		t.Fatalf("AddEvent 1: %v", err)
	}

	_, err = AddEvent(db, "event two", nil, []Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "personal"},
	})
	if err != nil {
		t.Fatalf("AddEvent 2: %v", err)
	}

	counts, err := ListMeta(db)
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}

	// Expect 3 distinct key=value pairs: author=alice (2), tag=personal (1), tag=work (1).
	if len(counts) != 3 {
		t.Fatalf("len(counts) = %d, want 3", len(counts))
	}

	// Verify ordering is by key, then value.
	// author=alice, tag=personal, tag=work
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
