package event

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
)

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
