package event

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(testDB(t))
}

func TestStore_NewStoreHoldsDB(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if s == nil || s.DB == nil {
		t.Fatal("NewStore returned a Store with no DB")
	}
}

func TestStore_AddAndGet(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, err := s.Add(ctx, "via store", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id < 1 {
		t.Fatalf("Add returned id=%d", id)
	}

	ev, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Text != "via store" {
		t.Errorf("text = %q, want %q", ev.Text, "via store")
	}
	if len(ev.Meta) != 1 || ev.Meta[0].Value != "alice" {
		t.Errorf("meta = %v, want [{author alice}]", ev.Meta)
	}
}

func TestStore_GetNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.Get(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get not-found err = %v, want ErrNotFound", err)
	}
}

func TestStore_DeleteAndHasChildren(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	parent, err := s.Add(ctx, "parent", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if _, err := s.Add(ctx, "child", &parent, nil, nil); err != nil {
		t.Fatalf("Add child: %v", err)
	}

	has, err := s.HasChildren(ctx, parent)
	if err != nil {
		t.Fatalf("HasChildren: %v", err)
	}
	if !has {
		t.Error("HasChildren = false, want true")
	}

	if err := s.Delete(ctx, parent); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, parent); !errors.Is(err, ErrNotFound) {
		t.Errorf("post-delete Get err = %v, want ErrNotFound", err)
	}
}

func TestStore_DeleteNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.Delete(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete not-found err = %v, want ErrNotFound", err)
	}
}

func TestStore_UpdateTextRefreshesFTS(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, err := s.Add(ctx, "before", nil, []parse.Meta{
		{Key: MetaKeyAuthor, Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newText := "after"
	if err := s.Update(ctx, id, &newText, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}

	matches, err := s.List(ctx, ListOpts{Filter: "after"})
	if err != nil {
		t.Fatalf("List after: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("FTS not updated: %d matches for 'after', want 1", len(matches))
	}
}

func TestStore_UpdateNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	text := "x"
	if err := s.Update(ctx, 9999, &text, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update not-found err = %v, want ErrNotFound", err)
	}
}

func TestStore_List(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "a", nil, []parse.Meta{
		{Key: MetaKeyTag, Value: "ops"},
	}, nil); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := s.Add(ctx, "b", nil, []parse.Meta{
		{Key: MetaKeyTag, Value: "work"},
	}, nil); err != nil {
		t.Fatalf("Add b: %v", err)
	}

	all, err := s.List(ctx, ListOpts{})
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("List all len = %d, want 2", len(all))
	}

	filtered, err := s.List(ctx, ListOpts{Filter: "#ops"})
	if err != nil {
		t.Fatalf("List filtered: %v", err)
	}
	if len(filtered) != 1 || filtered[0].Text != "a" {
		t.Errorf("filtered = %v, want [a]", filtered)
	}
}

func TestStore_GetSubtree(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	root, err := s.Add(ctx, "root", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add root: %v", err)
	}
	child, err := s.Add(ctx, "child", &root, nil, nil)
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	got, err := s.GetSubtree(ctx, root)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}
	if len(got) != 2 || got[0].ID != root || got[1].ID != child {
		t.Errorf("subtree IDs = %v, want [%d, %d]", got, root, child)
	}
}

func TestStore_GetSubtreeNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if _, err := s.GetSubtree(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSubtree not-found err = %v, want ErrNotFound", err)
	}
}

func TestStore_MetaCRUD(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	for _, v := range []string{"ops", "ops", "work"} {
		if _, err := s.Add(ctx, "x", nil, []parse.Meta{
			{Key: MetaKeyTag, Value: v},
		}, nil); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	counts, err := s.ListMeta(ctx, ListMetaOpts{})
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(counts) != 2 {
		t.Errorf("ListMeta len = %d, want 2", len(counts))
	}

	n, err := s.CountMeta(ctx, MetaKeyTag, "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if n != 2 {
		t.Errorf("CountMeta(tag, ops) = %d, want 2", n)
	}

	updated, err := s.UpdateMeta(ctx, MetaKeyTag, "ops", MetaKeyTag, "infra")
	if err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}
	if updated != 2 {
		t.Errorf("UpdateMeta affected = %d, want 2", updated)
	}

	deleted, err := s.DeleteMeta(ctx, MetaKeyTag, "infra")
	if err != nil {
		t.Fatalf("DeleteMeta: %v", err)
	}
	if deleted != 2 {
		t.Errorf("DeleteMeta affected = %d, want 2", deleted)
	}

	left, err := s.CountMeta(ctx, MetaKeyTag, "infra")
	if err != nil {
		t.Fatalf("CountMeta after delete: %v", err)
	}
	if left != 0 {
		t.Errorf("count after delete = %d, want 0", left)
	}
}

func TestStore_ListMeta_Filter(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	if _, err := s.Add(ctx, "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "people", Value: "alice"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := s.ListMeta(ctx, ListMetaOpts{Key: "people"})
	if err != nil {
		t.Fatalf("ListMeta: %v", err)
	}
	if len(got) != 1 || got[0].Key != "people" {
		t.Errorf("got %v, want one row with key=people", got)
	}
}

func TestStore_AddWithCreatedAtRoundTrips(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	want := time.Date(2024, 6, 1, 10, 30, 0, 0, time.UTC)
	id, err := s.Add(ctx, "ts", nil, nil, &want)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	ev, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ev.CreatedAt.Equal(want) {
		t.Errorf("created_at = %v, want %v", ev.CreatedAt, want)
	}
}

func TestStore_ListSeq(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	for i := range 3 {
		if _, err := s.Add(ctx, fmt.Sprintf("e%d", i), nil, nil, nil); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	var got []string
	for ev, err := range s.ListSeq(ctx, ListOpts{Ascending: true}) {
		if err != nil {
			t.Fatalf("ListSeq: %v", err)
		}
		got = append(got, ev.Text)
	}
	want := []string{"e0", "e1", "e2"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStore_Reparent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	a, _ := s.Add(ctx, "a", nil, nil, nil)
	b, _ := s.Add(ctx, "b", nil, nil, nil)
	c, _ := s.Add(ctx, "c", &a, nil, nil)

	if err := s.Reparent(ctx, c, &b); err != nil {
		t.Fatalf("Reparent: %v", err)
	}
	ev, err := s.Get(ctx, c)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.ParentID == nil || *ev.ParentID != b {
		t.Errorf("ParentID = %v, want %d", ev.ParentID, b)
	}
}

func TestStore_AddTags(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.Add(ctx, "x", nil, nil, nil)
	added, err := s.AddTags(ctx, id, []parse.Meta{{Key: "tag", Value: "ops"}})
	if err != nil {
		t.Fatalf("AddTags: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1", added)
	}
	n, err := s.CountMeta(ctx, "tag", "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if n != 1 {
		t.Errorf("count = %d, want 1", n)
	}
}

func TestStore_RemoveTags(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	id, _ := s.Add(ctx, "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil)

	n, err := s.RemoveTags(ctx, id, []parse.Meta{{Key: "tag", Value: "ops"}})
	if err != nil {
		t.Fatalf("RemoveTags: %v", err)
	}
	if n != 1 {
		t.Errorf("removed = %d, want 1", n)
	}
}
