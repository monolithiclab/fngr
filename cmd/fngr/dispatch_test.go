package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// dispatch parses argv, sets up the same bindings main() does, and runs the
// chosen command. Tests that go through this path catch wiring bugs (missing
// Kong bindings, wrong Run signatures, etc.) that direct cmd.Run() calls miss.
func dispatch(t *testing.T, argv []string, stdin string, isTTY bool) (string, error) {
	t.Helper()

	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("fngr"),
		kongVars("test", "tester"),
		kong.Exit(func(int) {}),
		kong.Writers(&bytes.Buffer{}, &bytes.Buffer{}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	kctx, err := parser.Parse(argv)
	if err != nil {
		return "", err
	}

	out := &bytes.Buffer{}
	kctx.BindTo(newTestStore(t), (*eventStore)(nil))
	kctx.Bind(ioStreams{
		In:    strings.NewReader(stdin),
		Out:   out,
		Err:   io.Discard,
		IsTTY: isTTY,
	})

	if err := kctx.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// TestKongDispatch_AllCommands is the regression test for the
// "couldn't find binding of type main.eventStore" bug. Every command must be
// reachable via Kong's full Parse + Run cycle, not just by calling cmd.Run()
// in isolation.
func TestKongDispatch_AllCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		argv  []string
		stdin string
		isTTY bool
		want  string
	}{
		{name: "bare-fngr", argv: []string{}, isTTY: true, want: ""},
		{name: "bare-fngr-reverse", argv: []string{"-r"}, isTTY: true, want: ""},
		{name: "bare-fngr-no-pager", argv: []string{"--no-pager"}, isTTY: true, want: ""},
		{name: "add", argv: []string{"add", "hello"}, isTTY: true, want: "Added event 1"},
		{name: "add-multiarg", argv: []string{"add", "deployed", "v1.2"}, isTTY: true, want: "Added event 1"},
		{name: "add-stdin", argv: []string{"add"}, stdin: "piped body", isTTY: false, want: ""},
		{name: "add-editor", argv: []string{"add", "-e"}, isTTY: true, want: "Added event 1"},
		{name: "add-json", argv: []string{"add", "--format=json"}, stdin: `{"title":"hi"}`, isTTY: false, want: ""},
		{name: "list", argv: []string{"list"}, isTTY: true, want: ""},
		{name: "event-bare", argv: []string{"event", "1"}, isTTY: true, want: ""},
		{name: "event-show", argv: []string{"event", "show", "1"}, isTTY: true, want: ""},
		{name: "event-show-tree", argv: []string{"event", "show", "1", "--tree"}, isTTY: true, want: ""},
		{name: "event-show-json", argv: []string{"event", "show", "1", "--format", "json"}, isTTY: true, want: ""},
		{name: "list-md", argv: []string{"list", "--format", "md"}, isTTY: true, want: ""},
		{name: "event-show-md", argv: []string{"event", "show", "1", "--format", "md"}, isTTY: true, want: ""},
		{name: "event-text", argv: []string{"event", "text", "1", "x"}, isTTY: true, want: ""},
		{name: "event-title", argv: []string{"event", "title", "1", "new title"}, isTTY: true, want: ""},
		{name: "event-body", argv: []string{"event", "body", "1", "new body"}, isTTY: true, want: ""},
		{name: "event-time", argv: []string{"event", "time", "1", "09:30"}, isTTY: true, want: ""},
		{name: "event-date", argv: []string{"event", "date", "1", "2026-05-01"}, isTTY: true, want: ""},
		{name: "event-attach", argv: []string{"event", "attach", "1", "2"}, isTTY: true, want: ""},
		{name: "event-detach", argv: []string{"event", "detach", "1"}, isTTY: true, want: ""},
		{name: "event-tag", argv: []string{"event", "tag", "1", "#ops"}, isTTY: true, want: ""},
		{name: "event-untag", argv: []string{"event", "untag", "1", "#ops"}, isTTY: true, want: ""},
		{name: "delete", argv: []string{"delete", "1", "-f"}, isTTY: true, want: ""},
		{name: "meta", argv: []string{"meta"}, isTTY: true, want: ""},
		{name: "meta-search-key", argv: []string{"meta", "-S", "tag"}, isTTY: true, want: ""},
		{name: "meta-search-keyvalue", argv: []string{"meta", "-S", "tag=a"}, isTTY: true, want: ""},
		{name: "meta-search-shorthand", argv: []string{"meta", "-S", "#a"}, isTTY: true, want: ""},
		{name: "meta-rename", argv: []string{"meta", "rename", "tag=a", "tag=b", "-f"}, isTTY: true, want: ""},
		{name: "meta-delete", argv: []string{"meta", "delete", "tag=a", "-f"}, isTTY: true, want: ""},
		{name: "help-bare", argv: []string{"help"}, isTTY: true, want: ""},
		{name: "help-cmd", argv: []string{"help", "add"}, isTTY: true, want: ""},
		{name: "help-verb-tree", argv: []string{"help", "event", "show"}, isTTY: true, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if tc.name == "add-editor" {
				origEditor := launchEditor
				launchEditor = func(string) (string, error) { return "from editor", nil }
				t.Cleanup(func() { launchEditor = origEditor })
			}

			_, err := dispatch(t, tc.argv, tc.stdin, tc.isTTY)
			if err != nil && strings.Contains(err.Error(), "couldn't find binding") {
				t.Fatalf("kong binding error for %q: %v", tc.argv, err)
			}
			// Other errors are fine; this test only guards the wiring contract.
		})
	}
}

// newDispatcher returns a run function that parses and executes argv against
// one shared store, so multi-command flows (add, then mutate, then list) see
// each other's writes. The plain `dispatch` helper builds a fresh store per
// call and is only good for single-shot wiring checks.
func newDispatcher(t *testing.T) func(argv []string) (string, error) {
	t.Helper()

	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("fngr"),
		kongVars("test", "tester"),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	store := newTestStore(t)
	return func(argv []string) (string, error) {
		kctx, err := parser.Parse(argv)
		if err != nil {
			return "", err
		}
		out := &bytes.Buffer{}
		kctx.BindTo(store, (*eventStore)(nil))
		kctx.Bind(ioStreams{In: strings.NewReader(""), Out: out, Err: io.Discard, IsTTY: true})
		err = kctx.Run()
		return out.String(), err
	}
}

// TestKongDispatch_AddTimePrefix covers both outcomes of the title time-prefix
// rule through the full Kong Parse + Run path, not just direct cmd.Run: a
// usable prefix is parsed and stripped, an unstorable one is left verbatim.
//
// The second case is the untrusted-content route. `fngr add` parses a leading
// ": "-delimited token out of piped text, so scraped text could previously
// write a year like -178954945 — a value SQLite stores but the driver cannot
// read back, making every later read of the database fail.
func TestKongDispatch_AddTimePrefix(t *testing.T) {
	t.Parallel()

	const scraped = "2147483647 months ago: meeting notes from a scraped page"
	tests := []struct {
		name    string
		title   string
		want    string
		notWant string
	}{
		{"usable prefix is stripped", "9:30: had coffee", "had coffee", "9:30: had coffee"},
		{"unstorable prefix stays in the title", scraped, scraped, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			run := newDispatcher(t)

			if _, err := run([]string{"add", tt.title}); err != nil {
				t.Fatalf("add: %v", err)
			}
			out, err := run([]string{"list", "--format", "flat"})
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("list output missing %q:\n%s", tt.want, out)
			}
			if tt.notWant != "" && strings.Contains(out, tt.notWant) {
				t.Errorf("list output still has %q:\n%s", tt.notWant, out)
			}

			// An unreadable stamp used to abort the whole result set, and
			// `delete` calls Get first — so there was no way to remove the row.
			if _, err := run([]string{"event", "1"}); err != nil {
				t.Fatalf("event 1: %v", err)
			}
			if _, err := run([]string{"delete", "1", "-f"}); err != nil {
				t.Fatalf("delete 1: %v", err)
			}
		})
	}
}

// TestKongDispatch_AddThenListEndToEnd exercises the full happy path through
// Kong twice against the same store, proving the dispatch + bindings handle
// stateful flows correctly.
func TestKongDispatch_AddThenListEndToEnd(t *testing.T) {
	t.Parallel()

	run := newDispatcher(t)

	if _, err := run([]string{"add", "first"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := run([]string{"list", "--format", "flat"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "first") {
		t.Errorf("list output missing 'first':\n%s", out)
	}
}

// TestKongDispatch_UnicodeMetaNames proves non-ASCII @person / #tag names
// survive the whole CLI round trip: body extraction on add, the `event tag`
// validator, and the `meta -S` filter all share one regex, and it used to be
// ASCII-only.
func TestKongDispatch_UnicodeMetaNames(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "coffee with @josé about #déploiement"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := run([]string{"event", "tag", "1", "@田中"}); err != nil {
		t.Fatalf("event tag: %v", err)
	}

	out, err := run([]string{"meta"})
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	for _, want := range []string{"josé", "déploiement", "田中"} {
		if !strings.Contains(out, want) {
			t.Errorf("meta output missing %q:\n%s", want, out)
		}
	}

	out, err = run([]string{"meta", "-S", "@josé"})
	if err != nil {
		t.Fatalf("meta -S @josé: %v", err)
	}
	if !strings.Contains(out, "josé") {
		t.Errorf("meta -S @josé missing the person:\n%s", out)
	}
	if strings.Contains(out, "田中") {
		t.Errorf("meta -S @josé should not list other people:\n%s", out)
	}
}

// TestKongDispatch_SearchFilter drives -S through the full CLI. The operand
// order in the negation cases is the point: `!#bugfix & #work` used to be
// evaluated as `!(#bugfix & #work)` and returned every event, while the
// equivalent `#work & !#bugfix` returned the right one.
func TestKongDispatch_SearchFilter(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	for _, title := range []string{
		"fix the session-handler crash #bugfix #work",
		"ship the release #work",
		"walk the dog #home",
	} {
		if _, err := run([]string{"add", title}); err != nil {
			t.Fatalf("add %q: %v", title, err)
		}
	}

	tests := []struct {
		name    string
		filter  string
		want    []string
		notWant []string
	}{
		// Only the two negation orders here: the rest of the grammar is
		// covered by TestList_FilterSemantics in internal/event, and this test
		// exists to prove the fix survives the Kong dispatch path.
		{"NOT first", "!#bugfix & #work", []string{"ship the release"}, []string{"session-handler", "walk the dog"}},
		{"NOT last", "#work & !#bugfix", []string{"ship the release"}, []string{"session-handler", "walk the dog"}},
	}
	for _, tt := range tests {
		// Not parallel: these subtests share one parser and store.
		t.Run(tt.name, func(t *testing.T) {
			out, err := run([]string{"list", "--format", "flat", "-S", tt.filter})
			if err != nil {
				t.Fatalf("list -S %q: %v", tt.filter, err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Errorf("-S %q output missing %q:\n%s", tt.filter, want, out)
				}
			}
			for _, bad := range tt.notWant {
				if strings.Contains(out, bad) {
					t.Errorf("-S %q output should not contain %q:\n%s", tt.filter, bad, out)
				}
			}
		})
	}

	// A bare "!" used to panic in the filter preprocessor, taking the CLI down
	// with it instead of reporting a syntax error.
	t.Run("bare NOT is a syntax error", func(t *testing.T) {
		if _, err := run([]string{"list", "--format", "flat", "-S", "!"}); err == nil {
			t.Fatal(`-S "!" succeeded, want a syntax error`)
		}
	})
}

// TestKongDispatch_OutOfRangeTimeFlagErrors proves --time rejects an offset
// that cannot be stored, instead of writing an unreadable row.
func TestKongDispatch_OutOfRangeTimeFlagErrors(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	_, err := run([]string{"add", "x", "--time", "1000000 days ago"})
	if err == nil {
		t.Fatal("add --time '1000000 days ago' succeeded, want an out-of-range error")
	}

	// Nothing was written, and the database still reads.
	out, listErr := run([]string{"list", "--format", "flat"})
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no events, got:\n%s", out)
	}
}
