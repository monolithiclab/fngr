package internal

import (
	"errors"
	"testing"
)

func TestAddEvent(t *testing.T) {
	db := testDBWithSchema(t)

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
	db := testDBWithSchema(t)

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
	db := testDBWithSchema(t)

	invalidParent := int64(9999)
	_, err := AddEvent(db, "orphan event", &invalidParent, nil)
	if err == nil {
		t.Fatal("expected error for invalid parent, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestGetEvent(t *testing.T) {
	db := testDBWithSchema(t)

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
	db := testDBWithSchema(t)

	_, err := GetEvent(db, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestDeleteEvent(t *testing.T) {
	db := testDBWithSchema(t)

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
	db := testDBWithSchema(t)

	err := DeleteEvent(db, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent event, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestDeleteEvent_CascadesChildren(t *testing.T) {
	db := testDBWithSchema(t)

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

func TestListEvents_NoFilter(t *testing.T) {
	db := testDBWithSchema(t)

	_, err := AddEvent(db, "first event #work", nil, []Meta{{Key: "tag", Value: "work"}})
	if err != nil {
		t.Fatalf("AddEvent 1: %v", err)
	}
	_, err = AddEvent(db, "second event #personal", nil, []Meta{{Key: "tag", Value: "personal"}})
	if err != nil {
		t.Fatalf("AddEvent 2: %v", err)
	}

	events, err := ListEvents(db, ListOpts{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
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

func TestListEvents_WithFilter(t *testing.T) {
	db := testDBWithSchema(t)

	_, err := AddEvent(db, "deploy to prod #ops", nil, []Meta{{Key: "tag", Value: "ops"}})
	if err != nil {
		t.Fatalf("AddEvent 1: %v", err)
	}
	_, err = AddEvent(db, "standup meeting #work", nil, []Meta{{Key: "tag", Value: "work"}})
	if err != nil {
		t.Fatalf("AddEvent 2: %v", err)
	}

	events, err := ListEvents(db, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "deploy to prod #ops" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "deploy to prod #ops")
	}
}

func TestListEvents_WithDateRange(t *testing.T) {
	db := testDBWithSchema(t)

	// Insert events with explicit timestamps to control ordering and filtering.
	_, err := db.Exec("INSERT INTO events (text, created_at) VALUES (?, ?)", "old event", "2026-01-01 00:00:00")
	if err != nil {
		t.Fatalf("insert old event: %v", err)
	}
	_, err = db.Exec("INSERT INTO events_fts (rowid, content) VALUES (1, 'old event')")
	if err != nil {
		t.Fatalf("insert old FTS: %v", err)
	}

	_, err = db.Exec("INSERT INTO events (text, created_at) VALUES (?, ?)", "new event", "2026-03-15 12:00:00")
	if err != nil {
		t.Fatalf("insert new event: %v", err)
	}
	_, err = db.Exec("INSERT INTO events_fts (rowid, content) VALUES (2, 'new event')")
	if err != nil {
		t.Fatalf("insert new FTS: %v", err)
	}

	// Filter to only include events on or after 2026-03-01.
	events, err := ListEvents(db, ListOpts{From: "2026-03-01"})
	if err != nil {
		t.Fatalf("ListEvents with From: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "new event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "new event")
	}

	// Filter to only include events on or before 2026-02-01.
	events, err = ListEvents(db, ListOpts{To: "2026-02-01"})
	if err != nil {
		t.Fatalf("ListEvents with To: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "old event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "old event")
	}

	// Filter with both From and To.
	events, err = ListEvents(db, ListOpts{From: "2026-02-01", To: "2026-04-01"})
	if err != nil {
		t.Fatalf("ListEvents with From and To: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Text != "new event" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "new event")
	}
}

func TestListMeta(t *testing.T) {
	db := testDBWithSchema(t)

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

func TestGetSubtree(t *testing.T) {
	db := testDBWithSchema(t)

	root, err := AddEvent(db, "root", nil, []Meta{{Key: MetaKeyAuthor, Value: "alice"}})
	if err != nil {
		t.Fatalf("AddEvent root: %v", err)
	}

	child, err := AddEvent(db, "child", &root, []Meta{{Key: MetaKeyAuthor, Value: "alice"}})
	if err != nil {
		t.Fatalf("AddEvent child: %v", err)
	}

	grandchild, err := AddEvent(db, "grandchild", &child, []Meta{{Key: MetaKeyAuthor, Value: "bob"}})
	if err != nil {
		t.Fatalf("AddEvent grandchild: %v", err)
	}

	// Add an unrelated event that should not appear in the subtree.
	if _, err := AddEvent(db, "unrelated", nil, nil); err != nil {
		t.Fatalf("AddEvent unrelated: %v", err)
	}

	events, err := GetSubtree(db, root)
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

	// Verify metadata is loaded.
	if len(events[2].Meta) != 1 || events[2].Meta[0].Value != "bob" {
		t.Errorf("grandchild meta = %v, want [{author bob}]", events[2].Meta)
	}
}

func TestGetSubtree_LeafNode(t *testing.T) {
	db := testDBWithSchema(t)

	id, err := AddEvent(db, "leaf", nil, nil)
	if err != nil {
		t.Fatalf("AddEvent: %v", err)
	}

	events, err := GetSubtree(db, id)
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
	db := testDBWithSchema(t)

	_, err := GetSubtree(db, 9999)
	if err == nil {
		t.Fatal("expected error for nonexistent root, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %q, want ErrNotFound", err.Error())
	}
}

func TestFTSIsolation_MetaTokensNotMatchedByBareWords(t *testing.T) {
	db := testDBWithSchema(t)

	// Event with tag=deploy in metadata but "deploy" does NOT appear in body text.
	_, err := AddEvent(db, "pushed to production", nil, []Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "deploy"},
	})
	if err != nil {
		t.Fatalf("AddEvent: %v", err)
	}

	// Bare word "deploy" should NOT match because the FTS token is "tag=deploy"
	// (the = is a token character), not "deploy" as a standalone token.
	events, err := ListEvents(db, ListOpts{Filter: "deploy"})
	if err != nil {
		t.Fatalf("ListEvents bare word: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("bare word 'deploy' matched %d events, want 0 (FTS isolation broken)", len(events))
	}

	// But #deploy (tag shorthand) should match via "tag=deploy" phrase.
	events, err = ListEvents(db, ListOpts{Filter: "#deploy"})
	if err != nil {
		t.Fatalf("ListEvents #deploy: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("#deploy matched %d events, want 1", len(events))
	}
}

func TestFTSIsolation_BodyWordsNotMatchedByMetaFilter(t *testing.T) {
	db := testDBWithSchema(t)

	// Event with "work" in body text but no tag=work metadata.
	_, err := AddEvent(db, "heading to work early", nil, []Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
	})
	if err != nil {
		t.Fatalf("AddEvent: %v", err)
	}

	// #work filter → "tag=work" phrase match. Should NOT match because
	// "work" in body is a standalone token, not "tag=work".
	events, err := ListEvents(db, ListOpts{Filter: "#work"})
	if err != nil {
		t.Fatalf("ListEvents #work: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("#work matched %d events, want 0 (body word leaked into meta filter)", len(events))
	}

	// Bare word "work" should match the body text.
	events, err = ListEvents(db, ListOpts{Filter: "work"})
	if err != nil {
		t.Fatalf("ListEvents bare work: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("bare 'work' matched %d events, want 1", len(events))
	}
}

func TestListEvents_ComplexFilters(t *testing.T) {
	db := testDBWithSchema(t)

	// Event 1: tagged ops, person alice, body "deploy to prod"
	_, err := AddEvent(db, "deploy to prod", nil, []Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "ops"},
		{Key: MetaKeyPeople, Value: "alice"},
	})
	if err != nil {
		t.Fatalf("AddEvent 1: %v", err)
	}

	// Event 2: tagged work, person bob, body "standup meeting"
	_, err = AddEvent(db, "standup meeting", nil, []Meta{
		{Key: MetaKeyAuthor, Value: "bob"},
		{Key: MetaKeyTag, Value: "work"},
		{Key: MetaKeyPeople, Value: "bob"},
	})
	if err != nil {
		t.Fatalf("AddEvent 2: %v", err)
	}

	// Event 3: tagged ops AND work, person alice, body "deploy standup"
	_, err = AddEvent(db, "deploy standup", nil, []Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
		{Key: MetaKeyTag, Value: "ops"},
		{Key: MetaKeyTag, Value: "work"},
		{Key: MetaKeyPeople, Value: "alice"},
	})
	if err != nil {
		t.Fatalf("AddEvent 3: %v", err)
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
			events, err := ListEvents(db, ListOpts{Filter: tt.filter})
			if err != nil {
				t.Fatalf("ListEvents(%q): %v", tt.filter, err)
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
