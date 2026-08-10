package main

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

func TestParseJSONAddInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		wantLen int
		wantErr string
	}{
		{name: "single-object", input: `{"title":"hi"}`, wantLen: 1},
		{name: "array-of-one", input: `[{"title":"hi"}]`, wantLen: 1},
		{name: "array-of-three", input: `[{"title":"a"},{"title":"b"},{"title":"c"}]`, wantLen: 3},
		{name: "empty-array", input: `[]`, wantLen: 0},
		{name: "malformed-json", input: `{"title":`, wantErr: "--format=json"},
		{name: "scalar-string", input: `"hello"`, wantErr: "--format=json"},
		{name: "scalar-number", input: `42`, wantErr: "--format=json"},
		{name: "with-meta", input: `{"title":"hi","meta":[["tag","ops"]]}`, wantLen: 1},
		{name: "with-parent-and-time", input: `{"title":"hi","parent_id":3,"created_at":"2026-04-01T12:00:00Z"}`, wantLen: 1},
		// `id` is what `fngr --format=json` emits, so it must survive a
		// straight pipe back in; it used to trip DisallowUnknownFields.
		{name: "id-is-accepted", input: `{"id":7,"title":"hi"}`, wantLen: 1},
		{name: "unknown-field-single", input: `{"title":"hi","ttile":"typo"}`, wantErr: "unknown field"},
		{name: "unknown-field-array", input: `[{"title":"hi","extra":1}]`, wantErr: "unknown field"},
		{name: "leading-whitespace-array", input: "  \n[{\"title\":\"hi\"}]", wantLen: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseJSONAddInput(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJSONAddInput: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("got %d records, want %d", len(got), tc.wantLen)
			}
		})
	}
}

// TestParseJSONAddInput_TypeErrorsNameTheWireShape pins the translation of
// encoding/json's type errors. Untranslated they read `cannot unmarshal object
// into Go struct field jsonAddInput.meta of type [][2]string` — a private Go
// type name and a Go declaration, in an error about a JSON file.
func TestParseJSONAddInput_TypeErrorsNameTheWireShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"object for an array field", `{"title":"t","meta":{"a":"b"}}`, `field "meta": got object, want array`},
		{"string for an array field", `{"title":"t","meta":"ops"}`, `field "meta": got string, want array`},
		{"number for a string field", `{"title":5}`, `field "title": got number, want string`},
		{"string for a number field", `{"title":"t","parent_id":"3"}`, `field "parent_id": got string, want number`},
		{"inside a batch", `[{"title":"a"},{"title":"b","id":"x"}]`, `field "id": got string, want number`},
		// No field name to prefix, and the case that leaked worst: untranslated
		// these read `cannot unmarshal number into Go value of type
		// main.jsonAddInput`, naming the private type outright.
		{"top-level scalar", `42`, `input: got number, want object`},
		{"top-level array of scalars", `["a"]`, `input: got string, want object`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseJSONAddInput(tc.input)
			if err == nil {
				t.Fatalf("parseJSONAddInput(%s) succeeded, want a type error", tc.input)
			}
			got := err.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("err = %q, want substring %q", got, tc.want)
			}
			if !strings.Contains(got, "at byte ") {
				t.Errorf("err = %q, want a byte offset — the field name alone cannot locate a record in a batch", got)
			}
			for _, leak := range []string{"jsonAddInput", "[][2]string", "Go struct", "Go value"} {
				if strings.Contains(got, leak) {
					t.Errorf("err = %q, leaks %q", got, leak)
				}
			}
		})
	}
}

// TestWireTypeError_PassesEverythingElseThrough guards the narrow gate: only a
// type mismatch is restated, so a syntax error and an unknown field keep the
// decoder's own wording rather than being flattened into one house message.
func TestWireTypeError_PassesEverythingElseThrough(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"syntax error", `{"title":`, "unexpected EOF"},
		{"unknown field", `{"title":"t","ttile":"typo"}`, `unknown field "ttile"`},
		{"invalid character", `{'title':1}`, "invalid character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseJSONAddInput(tc.input)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestWireTypeName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		val  any
		want string
	}{
		{"string", "", "string"},
		{"bool", false, "boolean"},
		{"int64", int64(0), "number"},
		{"float64", 0.0, "number"},
		{"uint8", uint8(0), "number"},
		{"slice", []string(nil), "array"},
		{"array", [2]string{}, "array"},
		{"struct", jsonAddInput{}, "object"},
		{"map", map[string]string(nil), "object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := wireTypeName(reflect.TypeOf(tc.val)); got != tc.want {
				t.Errorf("wireTypeName(%T) = %q, want %q", tc.val, got, tc.want)
			}
		})
	}
}

func TestJSONInputToAddInput(t *testing.T) {
	t.Parallel()

	mkPtr := func(s string) *string { return &s }
	mkInt64 := func(n int64) *int64 { return &n }

	cases := []struct {
		name       string
		in         jsonAddInput
		defaults   cliDefaults
		author     string
		wantTitle  string
		wantErr    string
		wantAuthor string // when set, asserts the merged meta has exactly this author value
	}{
		{
			name:      "happy-single",
			in:        jsonAddInput{Title: "hi"},
			author:    "alice",
			wantTitle: "hi",
		},
		{
			name:    "missing-title",
			in:      jsonAddInput{},
			author:  "alice",
			wantErr: "title is required",
		},
		{
			name:    "whitespace-only-title",
			in:      jsonAddInput{Title: "   "},
			author:  "alice",
			wantErr: "title is required",
		},
		{
			name:      "json-meta-overrides-cli",
			in:        jsonAddInput{Title: "x", Meta: [][2]string{{"env", "prod"}}},
			defaults:  cliDefaults{meta: []parse.Meta{{Key: "env", Value: "dev"}}},
			author:    "alice",
			wantTitle: "x",
		},
		{
			name:    "empty-meta-key",
			in:      jsonAddInput{Title: "x", Meta: [][2]string{{"", "v"}}},
			author:  "alice",
			wantErr: "meta[0]: empty key",
		},
		{
			name:    "empty-meta-value",
			in:      jsonAddInput{Title: "x", Meta: [][2]string{{"k", ""}}},
			author:  "alice",
			wantErr: `meta[0]: empty value for key "k"`,
		},
		{
			name:    "whitespace-in-meta-key",
			in:      jsonAddInput{Title: "x", Meta: [][2]string{{"a b", "c"}}},
			author:  "alice",
			wantErr: "meta[0]: key \"a b\" contains a space",
		},
		{
			name:    "bad-created-at",
			in:      jsonAddInput{Title: "x", CreatedAt: mkPtr("not-a-time")},
			author:  "alice",
			wantErr: "created_at",
		},
		{
			name:      "json-parent-id-overrides-cli",
			in:        jsonAddInput{Title: "x", ParentID: mkInt64(7)},
			defaults:  cliDefaults{parent: mkInt64(3)},
			author:    "alice",
			wantTitle: "x",
		},
		{
			name:      "valid-created-at",
			in:        jsonAddInput{Title: "x", CreatedAt: mkPtr("2026-04-01T12:00:00Z")},
			author:    "alice",
			wantTitle: "x",
		},
		{
			name:       "json-author-suppresses-default",
			in:         jsonAddInput{Title: "x", Meta: [][2]string{{"author", "bob"}}},
			author:     "alice",
			wantTitle:  "x",
			wantAuthor: "bob",
		},
		{
			// The import path takes meta as an arbitrary list of pairs, so it
			// is the one place a caller can hand over two authors without
			// going through a flag parser. Rejected rather than merged: the
			// event has one author and the file does not say which.
			name:    "two-json-authors-conflict",
			in:      jsonAddInput{Title: "x", Meta: [][2]string{{"author", "bob"}, {"author", "carol"}}},
			author:  "alice",
			wantErr: "exactly one author",
		},
		{
			name:    "no-author-from-any-source",
			in:      jsonAddInput{Title: "x"},
			author:  "",
			wantErr: "author is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := jsonInputToAddInput(tc.in, tc.defaults, tc.author, 0, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("jsonInputToAddInput: %v", err)
			}
			if got.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tc.wantTitle)
			}
			if tc.wantAuthor != "" {
				var authors []string
				for _, m := range got.Meta {
					if m.Key == "author" {
						authors = append(authors, m.Value)
					}
				}
				if len(authors) != 1 || authors[0] != tc.wantAuthor {
					t.Errorf("authors = %v, want exactly [%q]", authors, tc.wantAuthor)
				}
			}
		})
	}
}

func TestParseJSONAddInput_BatchSizeLimit(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i <= maxJSONBatchSize; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"title":"x"}`)
	}
	b.WriteByte(']')
	_, err := parseJSONAddInput(b.String())
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("err = %v, want 'exceeds limit'", err)
	}
}

func TestAddJSON_TitleBody(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO(`{"title":"deploy","body":"hotfix #ops"}`)

	cmd := &AddCmd{Args: []string{`{"title":"deploy","body":"hotfix #ops"}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if events[0].Title != "deploy" || events[0].Body != "hotfix #ops" {
		t.Errorf("got (title=%q, body=%q), want (deploy, hotfix #ops)", events[0].Title, events[0].Body)
	}
}

func TestAddJSON_BodyOptional(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{`{"title":"hello"}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, _ := s.List(context.Background(), event.ListOpts{})
	if events[0].Title != "hello" || events[0].Body != "" {
		t.Errorf("got (title=%q, body=%q), want (hello, '')", events[0].Title, events[0].Body)
	}
}

func TestAddJSON_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	cmd := &AddCmd{Args: []string{`{"title":""}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}

func TestAddJSON_RejectsTextField(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	cmd := &AddCmd{Args: []string{`{"text":"old"}`}, Author: "alice", Format: "json"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("expected unknown-field error on `text`, got %v", err)
	}
}

func TestIndexBySourceID(t *testing.T) {
	t.Parallel()
	id := func(n int64) *int64 { return &n }

	t.Run("maps ids to positions and skips records without one", func(t *testing.T) {
		t.Parallel()
		got, err := indexBySourceID([]jsonAddInput{
			{ID: id(9), Title: "a"},
			{Title: "no id"},
			{ID: id(4), Title: "c"},
		})
		if err != nil {
			t.Fatalf("indexBySourceID: %v", err)
		}
		if want := (map[int64]int{9: 0, 4: 2}); !maps.Equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("nil when no record carries an id", func(t *testing.T) {
		t.Parallel()
		got, err := indexBySourceID([]jsonAddInput{{Title: "a"}, {Title: "b"}})
		if err != nil || got != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("duplicate ids are ambiguous", func(t *testing.T) {
		t.Parallel()
		_, err := indexBySourceID([]jsonAddInput{{ID: id(1), Title: "a"}, {ID: id(1), Title: "b"}})
		if err == nil || !strings.Contains(err.Error(), "already used by record 0") {
			t.Errorf("err = %v, want a duplicate-id error naming record 0", err)
		}
	})
}

// TestAddJSON_ParentIDResolution is the C3 regression. A parent_id naming
// another record in the same batch used to be inserted as a literal integer
// into a database with its own autoincrement counter — so it either failed
// the parent-exists check or, worse, landed on an unrelated event and built a
// plausible but wrong tree.
func TestAddJSON_ParentIDResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// seed is added first, so the target database already has ids in use.
		seed  []string
		batch string
		// want maps a title to its expected parent's title ("" = root).
		want    map[string]string
		wantErr string
	}{
		{
			// Newest-first, the default `fngr --format=json` order: every
			// child is listed before the parent it points at.
			name:  "forward reference within the batch",
			batch: `[{"id":3,"title":"child","parent_id":2},{"id":2,"title":"root"}]`,
			want:  map[string]string{"child": "root", "root": ""},
		},
		{
			name:  "backward reference within the batch",
			batch: `[{"id":2,"title":"root"},{"id":3,"title":"child","parent_id":2}]`,
			want:  map[string]string{"child": "root", "root": ""},
		},
		{
			// The source ids collide with rows that already exist here. Before
			// the fix this silently reparented onto the seeded events.
			name:  "source ids collide with existing rows",
			seed:  []string{"seeded one", "seeded two"},
			batch: `[{"id":1,"title":"imported root"},{"id":2,"title":"imported child","parent_id":1}]`,
			want: map[string]string{
				"seeded one": "", "seeded two": "",
				"imported root": "", "imported child": "imported root",
			},
		},
		{
			// A parent_id that names no record in the batch still means "an id
			// in this database", which is how `add --parent` grafts onto an
			// existing tree.
			name:  "parent outside the batch is a target-database id",
			seed:  []string{"seeded one"},
			batch: `[{"title":"grafted","parent_id":1}]`,
			want:  map[string]string{"seeded one": "", "grafted": "seeded one"},
		},
		{
			name:    "parent outside the batch that does not exist",
			batch:   `[{"title":"orphan","parent_id":999}]`,
			wantErr: "parent event 999",
		},
		{
			name:    "cycle within the batch",
			batch:   `[{"id":1,"title":"a","parent_id":2},{"id":2,"title":"b","parent_id":1}]`,
			wantErr: "cycle",
		},
		{
			name:    "record parented to itself",
			batch:   `[{"id":1,"title":"a","parent_id":1}]`,
			wantErr: "cycle",
		},
		{
			// Two records claiming the same source id make every parent_id
			// naming it ambiguous, so the batch is rejected rather than
			// resolved last-one-wins.
			name:    "duplicate source ids",
			batch:   `[{"id":1,"title":"a"},{"id":1,"title":"b"}]`,
			wantErr: "id 1 already used by record 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			for _, title := range tt.seed {
				if _, err := s.Add(context.Background(), event.AddInput{
					Title: title, Meta: []parse.Meta{{Key: "author", Value: "alice"}},
				}); err != nil {
					t.Fatalf("seed %q: %v", title, err)
				}
			}

			io, _ := newTestIO("")
			cmd := &AddCmd{Args: []string{tt.batch}, Author: "alice", Format: "json"}
			err := cmd.Run(s, io)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			events, err := s.List(context.Background(), event.ListOpts{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			titleByID := make(map[int64]string, len(events))
			for _, ev := range events {
				titleByID[ev.ID] = ev.Title
			}
			got := make(map[string]string, len(events))
			for _, ev := range events {
				if ev.ParentID != nil {
					got[ev.Title] = titleByID[*ev.ParentID]
				} else {
					got[ev.Title] = ""
				}
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("parents = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAddJSON_FailedBatchWritesNothing pairs with the cases above: the
// parent-index check runs before the first INSERT, so a bad batch must not
// leave the earlier records behind.
func TestAddJSON_FailedBatchWritesNothing(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{
		Args:   []string{`[{"title":"good"},{"id":1,"title":"a","parent_id":2},{"id":2,"title":"b","parent_id":1}]`},
		Author: "alice", Format: "json",
	}
	if err := cmd.Run(s, io); err == nil {
		t.Fatal("Run succeeded, want a cycle error")
	}

	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("wrote %d events, want 0", len(events))
	}
}

// TestAddJSON_CreatedAtAcceptsCLILayouts pins created_at to timefmt.Parse
// rather than a bare RFC3339 parse, so an import file can use the same stamps
// as --time.
func TestAddJSON_CreatedAtAcceptsCLILayouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		stamp string
		want  string // in UTC, or "" to only require that it parses
	}{
		{"RFC3339 as emitted by --format=json", "2026-04-01T12:00:00Z", "2026-04-01 12:00:00"},
		{"space-separated", "2026-04-01 12:00:00", ""},
		{"date only", "2026-04-01", ""},
		{"no minutes", "2026-04-01T12:00", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			io, _ := newTestIO("")
			cmd := &AddCmd{
				Args:   []string{fmt.Sprintf(`{"title":"x","created_at":%q}`, tt.stamp)},
				Author: "alice", Format: "json",
			}
			if err := cmd.Run(s, io); err != nil {
				t.Fatalf("Run with created_at %q: %v", tt.stamp, err)
			}
			events, err := s.List(context.Background(), event.ListOpts{})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if tt.want == "" {
				return
			}
			if got := events[0].CreatedAt.UTC().Format(timefmt.DateTimeFormat); got != tt.want {
				t.Errorf("created_at = %q, want %q", got, tt.want)
			}
		})
	}
}
