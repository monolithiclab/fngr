package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

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
