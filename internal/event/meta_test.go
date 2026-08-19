package event

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

func assertMetaEqual(t *testing.T, got, want []parse.Meta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestAuthorOf covers the read side of the single-author rule, including the
// arbitrary answer it gives when the rule is broken — that answer is exactly
// why the write paths refuse to break it.
func TestAuthorOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		meta []parse.Meta
		want string
	}{
		{name: "nil", want: ""},
		{
			name: "no author key",
			meta: []parse.Meta{{Key: MetaKeyTag, Value: "ops"}},
			want: "",
		},
		{
			name: "author among others",
			meta: []parse.Meta{{Key: MetaKeyTag, Value: "ops"}, {Key: MetaKeyAuthor, Value: "nicolas"}},
			want: "nicolas",
		},
		{
			name: "two authors: the first wins, whichever that is",
			meta: []parse.Meta{{Key: MetaKeyAuthor, Value: "bob"}, {Key: MetaKeyAuthor, Value: "nicolas"}},
			want: "bob",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := AuthorOf(tt.meta); got != tt.want {
				t.Errorf("AuthorOf(%v) = %q, want %q", tt.meta, got, tt.want)
			}
		})
	}
}

// TestMergeMeta_Author is the M3 guard at the creation end. `author` is
// looked up by name at render time and the lookup returns whichever tuple
// sorts first, so two author rows on one event mean the displayed author is
// alphabetical rather than true. MergeMeta is the only place that can prevent
// the second row from ever being written.
func TestMergeMeta_Author(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		text          string
		explicit      []parse.Meta
		defaultAuthor string
		want          []parse.Meta
		wantErr       string
	}{
		{
			name:          "explicit author replaces the default",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "sarah"}},
			defaultAuthor: "nicolas",
			want:          []parse.Meta{{Key: "author", Value: "sarah"}},
		},
		{
			name:          "explicit author is ordered first",
			text:          "ship it #ops",
			explicit:      []parse.Meta{{Key: "env", Value: "prod"}, {Key: "author", Value: "sarah"}},
			defaultAuthor: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "sarah"},
				{Key: "tag", Value: "ops"},
				{Key: "env", Value: "prod"},
			},
		},
		{
			name:          "explicit author repeating the default is not a conflict",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "nicolas"}},
			defaultAuthor: "nicolas",
			want:          []parse.Meta{{Key: "author", Value: "nicolas"}},
		},
		{
			name:          "two distinct explicit authors conflict",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "sarah"}, {Key: "author", Value: "bob"}},
			defaultAuthor: "nicolas",
			wantErr:       "exactly one author",
		},
		{
			// The pair that slipped past a first draft comparing against
			// defaultAuthor instead of tracking "an explicit one was seen":
			// the first entry leaves author == defaultAuthor, so the second
			// looked like the only explicit one.
			name:          "two explicit authors, the first matching the default",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "nicolas"}, {Key: "author", Value: "bob"}},
			defaultAuthor: "nicolas",
			wantErr:       "exactly one author",
		},
		{
			name:          "no author at all when the default is empty",
			text:          "ship it #ops",
			defaultAuthor: "",
			want:          []parse.Meta{{Key: "tag", Value: "ops"}},
		},
		{
			// `-m author=` used to store a blank author *and* discard the
			// real one: the empty value suppressed the synthesised tuple and
			// then got appended by the explicit pass. No meta verb can repair
			// author, so the event was stuck that way.
			name:          "empty explicit author is rejected, not ignored",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: ""}},
			defaultAuthor: "nicolas",
			wantErr:       "author cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MergeMeta(tt.text, tt.explicit, tt.defaultAuthor)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("MergeMeta err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MergeMeta: %v", err)
			}
			assertMetaEqual(t, got, tt.want)
		})
	}
}

func TestCollectMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		flags   []string
		author  string
		want    []parse.Meta
		wantErr bool
	}{
		{
			name:   "combines all sources",
			text:   "Deploy done #ops @sarah",
			flags:  []string{"env=prod"},
			author: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "ops"},
				{Key: "env", Value: "prod"},
			},
		},
		{
			name:   "deduplicates across sources",
			text:   "#ops",
			flags:  []string{"tag=ops"},
			author: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "tag", Value: "ops"},
			},
		},
		{
			name:    "propagates flag parse error",
			text:    "some text",
			flags:   []string{"noequalssign"},
			author:  "nicolas",
			wantErr: true,
		},
		{
			name:   "author only",
			text:   "plain text",
			flags:  nil,
			author: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "nicolas"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CollectMeta(tt.text, tt.flags, tt.author)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CollectMeta expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CollectMeta unexpected error: %v", err)
			}
			assertMetaEqual(t, got, tt.want)
		})
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
