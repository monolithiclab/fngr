package event

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

func TestTokenizeFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  []token
	}{
		{"empty input", "", nil},
		{"blank input", "   \t ", nil},
		{"bare word", "project", []token{{tokTerm, "project", 1}}},
		{"hash tag", "#ops", []token{{tokTerm, "#ops", 1}}},
		{"at tag", "@sarah", []token{{tokTerm, "@sarah", 1}}},
		{"key=value", "tag=deploy", []token{{tokTerm, "tag=deploy", 1}}},
		{"AND operator", "a & b", []token{{tokTerm, "a", 1}, {tokAnd, "&", 3}, {tokTerm, "b", 5}}},
		{"OR operator", "a | b", []token{{tokTerm, "a", 1}, {tokOr, "|", 3}, {tokTerm, "b", 5}}},
		{"NOT is its own token", "!daily", []token{{tokNot, "!", 1}, {tokTerm, "daily", 2}}},
		{"trailing bang stays in the term", "wow!", []token{{tokTerm, "wow!", 1}}},
		{"operators need no spaces", "a&!b", []token{
			{tokTerm, "a", 1}, {tokAnd, "&", 2}, {tokNot, "!", 3}, {tokTerm, "b", 4},
		}},
		{"hierarchical tag", "#work/project-x", []token{{tokTerm, "#work/project-x", 1}}},
		// Positions are rune offsets, so a multi-byte term does not skew the
		// column reported in a later error message.
		{"position counts runes", "#déploiement & x", []token{
			{tokTerm, "#déploiement", 1}, {tokAnd, "&", 14}, {tokTerm, "x", 16},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tokenizeFilter(tt.input); !slices.Equal(got, tt.want) {
				t.Errorf("tokenizeFilter(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// shape replaces every occurrence of the (long, repeated) per-term MATCH
// subquery with "M" so the expected SQL below reads as pure boolean structure.
func shape(sql string) string { return strings.ReplaceAll(sql, filterMatch, "M") }

func TestCompileFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		wantSQL  string
		wantArgs []any
	}{
		{"bare word", "project", "M", []any{`content:"project"`}},
		{"hash shorthand", "#ops", "M", []any{`meta:"tag=ops"`}},
		{"at shorthand", "@sarah", "M", []any{`meta:"people=sarah"`}},
		{"key=value", "tag=deploy", "M", []any{`meta:"tag=deploy"`}},
		{"hierarchical tag", "#work/project-x", "M", []any{`meta:"tag=work/project-x"`}},

		// M7: a term is scoped to the column that can answer it. "Could this
		// have been stored as metadata?" is the same question the write paths
		// ask, so a key they accept must route to meta — a narrower rule here
		// would leave `-m ticket.id=PROJ-42` findable by nothing.
		{"a key the write path accepts is a meta term", "ticket.id=PROJ-42", "M",
			[]any{`meta:"ticket.id=PROJ-42"`}},
		{"leading = has no key, so it is content", "=oops", "M", []any{`content:"=oops"`}},
		{"empty value is still a meta term", "tag=", "M", []any{`meta:"tag="`}},
		{"prefix over every value of a key", "tag=*", "M", []any{`meta:"tag="*`}},

		// Terms are quoted on emit, so FTS5 never sees the punctuation as
		// syntax. Both of these used to reach SQLite as raw MATCH text: the
		// hyphen parsed as a column filter ("no such column: handler") and the
		// lone quote as an unterminated string.
		{"hyphen is text", "session-handler", "M", []any{`content:"session-handler"`}},
		{"lone double quote is text", `"`, "M", []any{`content:""""`}},
		{"embedded double quote", `tag=val"ue`, "M", []any{`meta:"tag=val""ue"`}},
		{"trailing bang is text", "wow!", "M", []any{`content:"wow!"`}},

		{"prefix search survives quoting", "sess*", "M", []any{`content:"sess"*`}},
		{"lone star is a term", "*", "M", []any{`content:"*"`}},

		{"explicit AND", "a & b", "(M AND M)", []any{`content:"a"`, `content:"b"`}},
		{"adjacent terms are AND", "deploy staging", "(M AND M)", []any{`content:"deploy"`, `content:"staging"`}},
		{"OR", "#ops | #deploy", "(M OR M)", []any{`meta:"tag=ops"`, `meta:"tag=deploy"`}},
		{"NOT", "!daily", "(NOT M)", []any{`content:"daily"`}},
		{"double NOT", "!!daily", "(NOT (NOT M))", []any{`content:"daily"`}},
		{"NOT with key=value", "!tag=deploy", "(NOT M)", []any{`meta:"tag=deploy"`}},

		// The C5 regression: negation used to be a leading "NOT " on the whole
		// MATCH string, which the query builder stripped and applied to
		// everything, so `!a & b` silently meant `!(a & b)`. Order must not
		// change the meaning.
		{"leading NOT binds to its own term", "!alpha & gamma",
			"((NOT M) AND M)", []any{`content:"alpha"`, `content:"gamma"`}},
		{"trailing NOT binds to its own term", "gamma & !alpha",
			"(M AND (NOT M))", []any{`content:"gamma"`, `content:"alpha"`}},

		{"AND binds tighter than OR", "a | b & c", "(M OR (M AND M))", []any{`content:"a"`, `content:"b"`, `content:"c"`}},
		{"OR chains left", "a | b | c", "((M OR M) OR M)", []any{`content:"a"`, `content:"b"`, `content:"c"`}},
		{"NOT binds tighter than AND", "!a & !b", "((NOT M) AND (NOT M))", []any{`content:"a"`, `content:"b"`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sql, args, err := compileFilter(tt.input)
			if err != nil {
				t.Fatalf("compileFilter(%q): %v", tt.input, err)
			}
			if got := shape(sql); got != tt.wantSQL {
				t.Errorf("compileFilter(%q) sql = %q, want %q", tt.input, got, tt.wantSQL)
			}
			if !slices.Equal(args, tt.wantArgs) {
				t.Errorf("compileFilter(%q) args = %v, want %v", tt.input, args, tt.wantArgs)
			}
		})
	}
}

func TestCompileFilter_Errors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantMsg string
	}{
		{"empty", "", "expression is empty"},
		{"blank", "  ", "expression is empty"},
		// A bare "!" used to index tok[0] on the empty remainder and panic.
		{"lone NOT", "!", "expected a search term at position 2"},
		{"repeated NOT", "!!!", "expected a search term at position 4"},
		{"leading AND", "& a", `unexpected "&" at position 1`},
		{"leading OR", "| a", `unexpected "|" at position 1`},
		{"trailing AND", "a &", "expected a search term at position 4"},
		{"trailing OR", "a | ", "expected a search term at position 5"},
		{"doubled AND", "a & & b", `unexpected "&" at position 5`},
		{"AND then OR", "a & | b", `unexpected "|" at position 5`},
		// Shorthand names are validated by parse.MetaArg, the same check
		// `event tag` and `meta -S` run, so an input one rejects can't be
		// silently accepted by the other. `@bob@example.com` used to compile
		// to a phrase that could never match and report "no results".
		{"bare hash", "#", `invalid #tag arg "#"`},
		{"bare at", "b & @", `invalid @person arg "@"`},
		{"hash prefix only", "#*", `invalid #tag arg "#"`},
		{"email is not a person name", "@bob@example.com", `invalid @person arg "@bob@example.com"`},
		{"position is reported for shorthands", "b & @", "at position 5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := compileFilter(tt.input)
			if err == nil {
				t.Fatalf("compileFilter(%q) = nil error, want one", tt.input)
			}
			if !errors.Is(err, ErrFilter) {
				t.Errorf("compileFilter(%q) error is not ErrFilter: %v", tt.input, err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("compileFilter(%q) = %q, want it to contain %q", tt.input, err, tt.wantMsg)
			}
		})
	}
}

// TestValidateFilter covers the pre-flight check streaming callers use. It has
// to reject exactly what compileFilter rejects — if the two ever disagree, a
// bad filter slips past validation and fails mid-stream, after the JSON
// renderer has already written "[".
func TestValidateFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid term", "#ops", false},
		{"valid expression", "!#bugfix & #work | deploy", false},
		{"empty", "", true},
		{"dangling operator", "a &", true},
		{"lone NOT", "!", true},
		{"bad shorthand", "@bob@example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateFilter(tt.input)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("ValidateFilter(%q) error = %v, want error: %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrFilter) {
				t.Errorf("ValidateFilter(%q) error is not ErrFilter: %v", tt.input, err)
			}
		})
	}
}

// TestList_FilterSemantics runs the compiled SQL against a real database. The
// unit tests above pin the emitted shape; this pins what the shape means.
func TestList_FilterSemantics(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	seed := []struct {
		title string
		meta  []parse.Meta
	}{
		{"fix the session-handler crash", []parse.Meta{
			{Key: MetaKeyTag, Value: "bugfix"}, {Key: MetaKeyTag, Value: "work"},
			{Key: MetaKeyPeople, Value: "bob"},
		}},
		{"ship the release", []parse.Meta{
			{Key: MetaKeyTag, Value: "work"}, {Key: MetaKeyPeople, Value: "alice"},
		}},
		{"walk the dog", []parse.Meta{{Key: MetaKeyTag, Value: "home"}}},
		{`quote " in the title`, nil},
	}
	for _, s := range seed {
		meta := append([]parse.Meta{{Key: MetaKeyAuthor, Value: "alice"}}, s.meta...)
		if _, err := Add(ctx, database, AddInput{Title: s.title, Meta: meta}); err != nil {
			t.Fatalf("seed %q: %v", s.title, err)
		}
	}

	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{"tag", "#work", []string{"fix the session-handler crash", "ship the release"}},
		{"negated tag", "!#work", []string{"walk the dog", `quote " in the title`}},
		// The C5 regression, both orders. `!#bugfix & #work` used to be read as
		// `!(#bugfix & #work)` and returned everything, including the bugfix.
		{"NOT first", "!#bugfix & #work", []string{"ship the release"}},
		{"NOT last", "#work & !#bugfix", []string{"ship the release"}},
		{"OR", "#home | #bugfix", []string{"fix the session-handler crash", "walk the dog"}},
		{"AND binds tighter than OR", "#home | #bugfix & #work",
			[]string{"fix the session-handler crash", "walk the dog"}},
		{"tag AND person", "#work & @alice", []string{"ship the release"}},
		{"person shorthand", "@bob", []string{"fix the session-handler crash"}},
		{"word AND tag", "the & #home", []string{"walk the dog"}},
		{"word NOT tag", "the & !#work", []string{"walk the dog", `quote " in the title`}},
		{"key=value", "author=alice", []string{
			"fix the session-handler crash", "ship the release", "walk the dog", `quote " in the title`,
		}},
		{"hyphenated term", "session-handler", []string{"fix the session-handler crash"}},
		// A lone quote used to abort the query with "unterminated string". It
		// now matches nothing, because FTS5's tokenizer drops punctuation — but
		// "no results" is an answer, not an error.
		{"lone quote is not an error", `"`, nil},
		{"prefix search", "releas*", []string{"ship the release"}},
		{"implicit AND", "walk dog", []string{"walk the dog"}},
		{"no match", "#work & #home", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			events, err := List(ctx, database, ListOpts{Filter: tt.filter, Ascending: true})
			if err != nil {
				t.Fatalf("List(%q): %v", tt.filter, err)
			}
			got := make([]string, len(events))
			for i, e := range events {
				got[i] = e.Title
			}
			slices.Sort(got)
			want := slices.Clone(tt.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("List(%q) = %v, want %v", tt.filter, got, want)
			}
		})
	}
}

// TestList_FilterErrorPropagates proves a parse failure reaches the caller
// instead of being handed to SQLite (or silently dropped by the iterator).
func TestList_FilterErrorPropagates(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	_, err := List(ctx, database, ListOpts{Filter: "a & "})
	if !errors.Is(err, ErrFilter) {
		t.Errorf("List error = %v, want ErrFilter", err)
	}
}
