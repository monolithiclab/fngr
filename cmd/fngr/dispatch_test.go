package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
)

// dispatch parses argv, sets up the same bindings main() does, and runs the
// chosen command against a store of its own. Tests that go through this path
// catch wiring bugs (missing Kong bindings, wrong Run signatures, etc.) that
// direct cmd.Run() calls miss.
func dispatch(t *testing.T, argv []string, stdin string, isTTY bool) (string, error) {
	t.Helper()
	return newDispatcherIO(t, stdin, isTTY)(argv)
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
				stubEditor(t, func(string) (string, error) { return "from editor", nil })
			}

			_, err := dispatch(t, tc.argv, tc.stdin, tc.isTTY)
			if err != nil && strings.Contains(err.Error(), "couldn't find binding") {
				t.Fatalf("kong binding error for %q: %v", tc.argv, err)
			}
			// Other errors are fine; this test only guards the wiring contract.
		})
	}
}

// newDispatcher returns a run function for the common interactive case: empty
// stdin, TTY.
func newDispatcher(t *testing.T) func(argv []string) (string, error) {
	t.Helper()
	return newDispatcherIO(t, "", true)
}

// newDispatcherIO returns a run function that parses and executes argv against
// one shared store, so multi-command flows (add, then mutate, then list) see
// each other's writes. stdin and isTTY are spelled out for tests of piped
// bodies and of non-interactive behaviour with nothing on stdin (cron, CI,
// `</dev/null`). It is the one place the Kong wiring lives; `dispatch` and
// `newDispatcher` are wrappers around it.
func newDispatcherIO(t *testing.T, stdin string, isTTY bool) func(argv []string) (string, error) {
	t.Helper()
	run, _ := newDispatcherOn(t, newTestStore(t), stdin, isTTY)
	return run
}

// newDispatcherErr is newDispatcherIO with stderr kept instead of discarded,
// for the warnings fngr writes there rather than failing on.
func newDispatcherErr(t *testing.T, stdin string, isTTY bool) (func(argv []string) (string, error), *bytes.Buffer) {
	t.Helper()
	return newDispatcherOn(t, newTestStore(t), stdin, isTTY)
}

// newDispatcherOn is newDispatcherIO over a caller-supplied store, for tests
// that also have to reach past the CLI to the database — forging a corrupt
// parent chain, say, which no fngr command can produce.
// It returns the run function and the buffer stderr is bound to, which
// accumulates across runs.
func newDispatcherOn(t *testing.T, store *event.Store, stdin string, isTTY bool) (func(argv []string) (string, error), *bytes.Buffer) {
	t.Helper()

	// Discard writers keep Kong's usage/error output out of the test log.
	parser := newTestParser(t, &bytes.Buffer{}, &bytes.Buffer{}, nil)

	errBuf := &bytes.Buffer{}
	return func(argv []string) (string, error) {
		kctx, err := parser.Parse(argv)
		if err != nil {
			return "", err
		}
		out := &bytes.Buffer{}
		kctx.BindTo(store, (*eventStore)(nil))
		kctx.Bind(ioStreams{
			In:    strings.NewReader(stdin),
			Out:   out,
			Err:   errBuf,
			IsTTY: isTTY,
		})
		err = kctx.Run()
		return out.String(), err
	}, errBuf
}

// TestKongDispatch_PromptsRefuseEmptyStdin is the H5 regression guard. A
// prompt reading a closed stdin got io.EOF and an empty answer, which confirm
// treated as "just pressed enter" — so every prompting verb ran its default.
// `meta rename` defaults to yes, meaning a cron job or CI step that forgot -f
// silently rewrote metadata across every event and reported success. Nothing
// may run unattended without -f now.
func TestKongDispatch_PromptsRefuseEmptyStdin(t *testing.T) {
	t.Parallel()
	run := newDispatcherIO(t, "", false)

	if _, err := run([]string{"add", "seed task #wip"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, argv := range [][]string{
		{"meta", "rename", "#wip", "#done"},
		{"meta", "delete", "#wip"},
		{"delete", "1"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			out, err := run(argv)
			if !errors.Is(err, errNoAnswer) {
				t.Fatalf("err = %v, want errNoAnswer (out %q)", err, out)
			}
		})
	}

	// Whatever the prompts refused to do must genuinely not have happened.
	out, err := run([]string{"meta", "-S", "tag"})
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if !strings.Contains(out, "tag=wip") {
		t.Errorf("meta output = %q, want tag=wip intact", out)
	}
	if _, err := run([]string{"event", "1"}); err != nil {
		t.Errorf("event 1: %v, want it to still exist", err)
	}
}

// TestKongDispatch_EditFlagRequiresTerminal is the H7 regression guard at the
// dispatch layer: `fngr add -e` piped into from a script must fail loudly
// instead of launching an editor with nowhere to run.
func TestKongDispatch_EditFlagRequiresTerminal(t *testing.T) {
	// NOTE: no t.Parallel() — stubEditor swaps package-level state.
	run := newDispatcherIO(t, "piped body that matters", false)

	forbidEditor(t)

	if _, err := run([]string{"add", "-e"}); !errors.Is(err, errEditNeedsTTY) {
		t.Fatalf("err = %v, want errEditNeedsTTY", err)
	}

	// Nothing was written.
	out, listErr := run([]string{"list", "--format", "flat"})
	if listErr != nil {
		t.Fatalf("list: %v", listErr)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no events, got:\n%s", out)
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

// TestKongDispatch_ControlBytesEscapedInOutput drives the render sanitizer
// through the real CLI. Text arrives from a file, a pipe or an editor, so a
// title carrying a newline is entirely storable — and it used to print as a
// second line indistinguishable from a real event, with any ANSI sequence
// beside it reaching the terminal untouched.
func TestKongDispatch_ControlBytesEscapedInOutput(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	const forged = "benign note\n99  4.00pm  root  SYSTEM all clear\x1b[2J"
	if _, err := run([]string{"add", forged}); err != nil {
		t.Fatalf("add: %v", err)
	}

	for _, tc := range []struct {
		name      string
		argv      []string
		wantLines int
	}{
		{"list flat", []string{"--format=flat"}, 1},
		{"list tree", []string{"--format=tree"}, 1},
		// ID / Date / Title / Meta: / author. No body block — the newline
		// landed in the title, which is exactly the case that must not
		// become a second line.
		{"event detail", []string{"event", "1"}, 5},
	} {
		// Subtests share one parser and one store, so they run in sequence.
		t.Run(tc.name, func(t *testing.T) {
			out, err := run(tc.argv)
			if err != nil {
				t.Fatalf("%v: %v", tc.argv, err)
			}
			if strings.ContainsRune(out, 0x1b) {
				t.Errorf("%v emitted a raw ESC:\n%q", tc.argv, out)
			}
			if got := strings.Count(out, "\n"); got != tc.wantLines {
				t.Errorf("%v produced %d lines, want %d:\n%q", tc.argv, got, tc.wantLines, out)
			}
		})
	}
}

// TestKongDispatch_RawC1FromStdin covers the byte a rune-oriented sanitizer
// cannot see. 0x9b is the 8-bit CSI introducer — a terminal acts on it just
// as it acts on ESC-[ — and it is not valid UTF-8, so ranging over the string
// decodes it to utf8.RuneError and lets it straight through.
//
// It has to arrive on a pipe: Kong rewrites invalid UTF-8 in argv to U+FFFD,
// while SQLite stores whatever bytes it is handed and gives them back
// verbatim.
func TestKongDispatch_RawC1FromStdin(t *testing.T) {
	t.Parallel()
	run := newDispatcherIO(t, "piped note \x9b31m tail", false)

	if _, err := run([]string{"add"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := run([]string{"--format=flat"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if strings.IndexByte(out, 0x9b) >= 0 {
		t.Errorf("a raw 0x9b reached the terminal:\n%q", out)
	}
	if !strings.Contains(out, `\x9b`) {
		t.Errorf("the byte was dropped or replaced instead of escaped:\n%q", out)
	}
}

// TestKongDispatch_ControlBytesEscapedInMetaList covers the one human-facing
// listing that lays out its own columns instead of going through render. Meta
// values are not limited to what the body-tag regex accepts — `--meta`
// takes any string — so this path needs the same escaping, applied before the
// widths are measured or the padding lands in the wrong place.
func TestKongDispatch_ControlBytesEscapedInMetaList(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "release notes", "--meta", "tag=ops\x1b[2Jfake"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := run([]string{"add", "second note", "--meta", "tag=short"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, err := run([]string{"meta"})
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("meta emitted a raw ESC:\n%q", out)
	}

	// One line per distinct key=value: the author both events share, plus
	// the two tags. A smuggled newline would add a fourth.
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("meta produced %d lines, want 3:\n%q", len(lines), out)
	}
	// Widths are measured on the escaped strings, so the count column stays
	// in one place.
	for _, l := range lines[1:] {
		if len(l) != len(lines[0]) {
			t.Errorf("meta columns are misaligned:\n%s", out)
			break
		}
	}
}

// TestKongDispatch_MetaRenameMerges drives the consolidation case end to end:
// two tags collapsed into one while an event carries both. It used to fail
// with a raw SQLite UNIQUE error and rename nothing at all, including the
// events that had no conflict.
func TestKongDispatch_MetaRenameMerges(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "alpha task #wip"}); err != nil {
		t.Fatalf("add alpha: %v", err)
	}
	if _, err := run([]string{"add", "beta task #wip #done"}); err != nil {
		t.Fatalf("add beta: %v", err)
	}

	out, err := run([]string{"meta", "rename", "#wip", "#done", "-f"})
	if err != nil {
		t.Fatalf("meta rename: %v", err)
	}
	if !strings.Contains(out, "Renamed 2 occurrences") {
		t.Errorf("meta rename said %q, want 2 occurrences", strings.TrimSpace(out))
	}

	// One line, because tag=wip is gone; count 2, because the event that
	// held both tags must not end up with a duplicate row.
	out, err = run([]string{"meta", "-S", "tag"})
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if got := strings.TrimSpace(out); got != "tag=done  (2)" {
		t.Errorf("meta -S tag = %q, want %q", got, "tag=done  (2)")
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

// TestKongDispatch_ForgedTagInBody is the M7 guard at the CLI: a note that
// merely spells out `#ops` as `tag=ops` used to answer a tag search as
// squarely as a tagged event, so an imported note could put itself into any
// tag view. Body and metadata are separate FTS columns now.
func TestKongDispatch_ForgedTagInBody(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "real note #ops"}); err != nil {
		t.Fatalf("add tagged: %v", err)
	}
	if _, err := run([]string{"add", "fake note. mentions tag=ops literally"}); err != nil {
		t.Fatalf("add forged: %v", err)
	}

	out, err := run([]string{"list", "--format", "flat", "-S", "#ops"})
	if err != nil {
		t.Fatalf("list -S #ops: %v", err)
	}
	if !strings.Contains(out, "real note") {
		t.Errorf("-S '#ops' missed the tagged event:\n%s", out)
	}
	if strings.Contains(out, "fake note") {
		t.Errorf("-S '#ops' matched a body that only says so:\n%s", out)
	}

	// The words are still findable as words.
	out, err = run([]string{"list", "--format", "flat", "-S", "literally"})
	if err != nil {
		t.Fatalf("list -S literally: %v", err)
	}
	if !strings.Contains(out, "fake note") {
		t.Errorf("-S 'literally' missed the event that says it:\n%s", out)
	}
}

// TestKongDispatch_CorruptParentChain is the M2 guard at the CLI. A cyclic
// parent chain — which fngr cannot write, but which `db.ResolvePath` will
// happily pick up from someone else's `.fngr.db` in the current directory —
// used to produce, in order: an empty listing at exit 0, a subtree query that
// spun until killed, and an attach that did the same inside an open
// transaction. Every event must be shown, and the two traversals must come
// back with an error naming the problem.
func TestKongDispatch_CorruptParentChain(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	run, _ := newDispatcherOn(t, store, "", true)

	for _, argv := range [][]string{
		{"add", "first half"},
		{"add", "second half", "--parent", "1"},
		{"add", "bystander"},
	} {
		if _, err := run(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}
	forgeParent(t, store, 1, 2)

	out, err := run([]string{"--no-pager"})
	if err != nil {
		t.Fatalf("bare fngr: %v", err)
	}
	for _, want := range []string{"first half", "second half", "bystander"} {
		if !strings.Contains(out, want) {
			t.Errorf("bare fngr dropped %q from the listing:\n%s", want, out)
		}
	}

	// Both traversals must fail loudly, and each must name the way out —
	// an error that only says "corrupt" leaves the user with a database
	// they cannot read and no next move.
	for _, tt := range []struct {
		name string
		argv []string
	}{
		{"subtree", []string{"event", "1", "-t"}},
		{"attach", []string{"event", "attach", "3", "1"}},
	} {
		_, err := run(tt.argv)
		if !errors.Is(err, event.ErrCorruptTree) {
			t.Errorf("%s err = %v, want ErrCorruptTree", tt.name, err)
			continue
		}
		if !strings.Contains(err.Error(), "fngr event detach") {
			t.Errorf("%s err = %q, want it to point at detach", tt.name, err)
		}
	}
}

// TestKongDispatch_TagSurvivesBodyEdit walks the M1 report verbatim through
// the CLI: tag an event with a handle, mention the handle in its body, then
// edit the mention away. The tag was silently gone at the end.
func TestKongDispatch_TagSurvivesBodyEdit(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "plain note"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	for _, argv := range [][]string{
		{"event", "tag", "1", "@bob"},
		{"event", "body", "1", "now mentions @bob inline"},
		{"event", "body", "1", "no more mention"},
	} {
		if _, err := run(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}

	out, err := run([]string{"event", "1"})
	if err != nil {
		t.Fatalf("event 1: %v", err)
	}
	if !strings.Contains(out, "people=bob") {
		t.Errorf("people=bob is gone after the body edit:\n%s", out)
	}
}

// TestKongDispatch_AuthorIsNotMutable is the M3 guard at the CLI boundary.
// Every meta verb that could give an event a second `author` or take its only
// one away has to refuse — the renderers read the key by name and show
// whichever row sorts first. Correcting the value in place is the one
// permitted edit, since it leaves every event with the row it already had;
// TestKongDispatch_AuthorValueIsCorrectable covers that side.
func TestKongDispatch_AuthorIsNotMutable(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "a note", "--author", "nicolas"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	for _, argv := range [][]string{
		{"event", "tag", "1", "author=evil"},
		{"event", "untag", "1", "author=nicolas"},
		{"meta", "rename", "author=nicolas", "people=nicolas", "-f"},
		{"meta", "delete", "author=nicolas", "-f"},
	} {
		// Not parallel: the subtests share one store, and each asserts on
		// the state the others must not have changed.
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			if _, err := run(argv); err == nil {
				t.Fatal("succeeded, want a protected-key rejection")
			}
		})
	}

	// `meta rename tag=x author=…` targets the protected key from the other
	// side: the source tuple is renameable, the destination is not.
	if _, err := run([]string{"event", "tag", "1", "#wip"}); err != nil {
		t.Fatalf("event tag #wip: %v", err)
	}
	if _, err := run([]string{"meta", "rename", "#wip", "author=evil", "-f"}); err == nil {
		t.Fatal("meta rename onto author succeeded, want a protected-key rejection")
	}

	out, err := run([]string{"meta", "-S", "author"})
	if err != nil {
		t.Fatalf("meta -S author: %v", err)
	}
	if got := strings.TrimSpace(out); got != "author=nicolas  (1)" {
		t.Errorf("meta -S author = %q, want exactly one unchanged author", got)
	}
}

// TestKongDispatch_AuthorValueIsCorrectable is the counterpart: a misspelled
// --author has to be fixable. Protecting the key against every verb made it
// the one field nothing could repair, and re-adding the event to correct a
// typo loses its id and its children.
func TestKongDispatch_AuthorValueIsCorrectable(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "a note", "--author", "nicolass"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := run([]string{"meta", "rename", "author=nicolass", "author=nicolas", "-f"}); err != nil {
		t.Fatalf("meta rename author value: %v", err)
	}

	out, err := run([]string{"meta", "-S", "author"})
	if err != nil {
		t.Fatalf("meta -S author: %v", err)
	}
	if got := strings.TrimSpace(out); got != "author=nicolas  (1)" {
		t.Errorf("meta -S author = %q, want the corrected spelling", got)
	}
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

// TestKongDispatch_NegativeLimitErrors follows the suggestion Kong's own error
// makes. `fngr -n -1` is refused as a malformed short flag with `perhaps try
// --limit="-1"?`, and taking that advice used to list the whole journal at
// exit 0, since the store only tests `Limit > 0`.
func TestKongDispatch_NegativeLimitErrors(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "one"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, err := run([]string{"list", `--limit=-1`})
	if err == nil {
		t.Fatalf("--limit=-1 succeeded, want an error; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--limit: limit cannot be negative") {
		t.Errorf("error = %v, want it to name the flag and the rule", err)
	}

	// Zero still means no limit, so the guard did not move the boundary.
	out, err = run([]string{"list", "--limit=0", "--format", "flat"})
	if err != nil {
		t.Fatalf("--limit=0: %v", err)
	}
	if !strings.Contains(out, "one") {
		t.Errorf("--limit=0 listed nothing, want the event; got:\n%s", out)
	}
}

// TestKongDispatch_UnstorableMetaRefusedEverywhere walks every entry point
// that mints a tuple, because a rule only one of them enforces is a rule the
// others route around. The shapes are the ones fngr can write but never search
// back: an empty value, and a key that is not exactly one -S term — whitespace
// or `&`/`|` split it into several, and a leading `!` turns the search for it
// into its complement, which is worse than no match at all.
func TestKongDispatch_UnstorableMetaRefusedEverywhere(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	if _, err := run([]string{"add", "seed", "-m", "tag=wip"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, tt := range []struct {
		name string
		args []string
	}{
		{"add --meta empty value", []string{"add", "x", "-m", "k="}},
		{"add --meta whitespace key", []string{"add", "x", "-m", "a b=c"}},
		{"add --meta and key", []string{"add", "x", "-m", "a&b=c"}},
		{"add --meta or key", []string{"add", "x", "-m", "a|b=c"}},
		{"add --meta negated key", []string{"add", "x", "-m", "!k=v"}},
		{"event tag empty value", []string{"event", "tag", "1", "k="}},
		{"event tag whitespace key", []string{"event", "tag", "1", "a b=c"}},
		{"event tag and key", []string{"event", "tag", "1", "a&b=c"}},
		{"event tag negated key", []string{"event", "tag", "1", "!k=v"}},
		{"meta rename onto empty value", []string{"meta", "rename", "tag=wip", "tag=", "-f"}},
		{"meta rename onto whitespace key", []string{"meta", "rename", "tag=wip", "a b=c", "-f"}},
		{"meta rename onto negated key", []string{"meta", "rename", "tag=wip", "!k=v", "-f"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := run(tt.args); err == nil {
				t.Errorf("%v succeeded, want a rejection", tt.args)
			}
		})
	}

	// The seed survived every refusal.
	out, err := run([]string{"meta"})
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if !strings.Contains(out, "tag=wip") {
		t.Errorf("meta listing lost the seed:\n%s", out)
	}
}

// TestKongDispatch_LegacyMetaStaysRemovable is the other half of the rule
// above. Every released build could write `k=` rows, and CLAUDE.md names
// `event untag 'k='` as the only verb that can name one — so gating the
// *naming* verbs on the same rule would make exactly the rows this change
// stops creating into rows nothing can remove.
func TestKongDispatch_LegacyMetaStaysRemovable(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	run, _ := newDispatcherOn(t, store, "", true)

	if _, err := run([]string{"add", "x"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Planted directly, the way forgeParent plants a corrupt tree: no current
	// write path produces this row, and every older one could.
	if _, err := store.DB.Exec(
		"INSERT INTO event_meta (event_id, key, value, source) VALUES (1, 'k', '', 'explicit')"); err != nil {
		t.Fatalf("plant legacy row: %v", err)
	}

	out, err := run([]string{"event", "untag", "1", "k="})
	if err != nil {
		t.Fatalf("event untag 'k=': %v", err)
	}
	if !strings.Contains(out, "1 removed") {
		t.Errorf("untag output = %q, want it to report one removal", out)
	}
}

// TestKongDispatch_MetaFilterAnyKey pins `fngr meta -S <key>` as a query path,
// not a minting one. It reaches ListMeta's plain `WHERE key = ?`, where the -S
// expression tokenizer is never involved — so no key is unmatchable here, and
// gating it would hide exactly the rows the mint rule refuses to create.
//
// It used to test MetaNameRe, so `ticket.id` — a key `-m` stores and `-S`
// finds — was refused by the one command that lists metadata.
func TestKongDispatch_MetaFilterAnyKey(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	run, _ := newDispatcherOn(t, store, "", true)

	if _, err := run([]string{"add", "x", "-m", "ticket.id=PROJ-42"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	out, err := run([]string{"meta", "-S", "ticket.id"})
	if err != nil {
		t.Fatalf("meta -S ticket.id: %v", err)
	}
	if !strings.Contains(out, "ticket.id=PROJ-42") {
		t.Errorf("meta -S ticket.id = %q, want the entry", out)
	}

	// A key no current write path can produce is still listable, which is the
	// whole point: finding it is how the operator learns it is there.
	if _, err := store.DB.Exec(
		"INSERT INTO event_meta (event_id, key, value, source) VALUES (1, 'a b', 'c', 'explicit')"); err != nil {
		t.Fatalf("plant legacy row: %v", err)
	}
	out, err = run([]string{"meta", "-S", "a b"})
	if err != nil {
		t.Fatalf("meta -S 'a b': %v", err)
	}
	if !strings.Contains(out, "a b=c") {
		t.Errorf("meta -S 'a b' = %q, want the planted entry", out)
	}
}

// TestKongDispatch_MarkdownAlias checks that the alias reaches the renderer
// through Kong's enum on every command that offers `md`. `markdown` is the
// word people reach for, and it was rejected at parse time.
func TestKongDispatch_MarkdownAlias(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)
	if _, err := run([]string{"add", "an entry"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, argv := range [][]string{
		{"list", "--format", "markdown", "--no-pager"},
		{"event", "show", "1", "--format", "markdown"},
	} {
		out, err := run(argv)
		if err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		if !strings.HasPrefix(out, "## ") {
			t.Errorf("%v produced %q, want markdown", argv, out)
		}
	}
}

// TestKongDispatch_SingleEventJSONIsAnObject pins the shape of
// `fngr event N --format=json`: one event is one object, so `jq '.title'`
// answers. It emitted a one-element array, against which every field query
// returned null.
func TestKongDispatch_SingleEventJSONIsAnObject(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)
	if _, err := run([]string{"add", "an entry"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := run([]string{"event", "show", "1", "--format", "json"})
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not a JSON object: %v\noutput:\n%s", err, out)
	}
	if got["title"] != "an entry" {
		t.Errorf("title = %v, want %q; output:\n%s", got["title"], "an entry", out)
	}
}

// TestKongDispatch_JSONRoundTrip exercises the recipe the README promises —
// `fngr --format=json | fngr add --format=json` — end to end through Kong,
// against two separate databases whose id counters have nothing in common.
//
// It used to fail three ways: the `id` field tripped DisallowUnknownFields;
// stripping `id` then failed the parent-exists check and rolled the batch
// back; and working around both produced exit 0 with a silently different
// tree, because parent_id was carried over as a literal integer.
func TestKongDispatch_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	source := newDispatcher(t)
	// Seed a two-level tree plus unrelated roots. The destination store is
	// pre-seeded with a different number of events so the id ranges diverge —
	// with matching counters a broken import can still look correct.
	for _, argv := range [][]string{
		{"add", "standup with @alice #work"},
		{"add", "deployed v1.2 #ops"},
		{"add", "rollback needed", "--parent", "2"},
		{"add", "postmortem scheduled", "--parent", "3"},
		{"add", "lunch", "--author", "sarah"},
	} {
		if _, err := source(argv); err != nil {
			t.Fatalf("seed %v: %v", argv, err)
		}
	}

	exported, err := source([]string{"list", "--format", "json", "--no-pager"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	dest := newDispatcher(t)
	for _, title := range []string{"pre-existing a", "pre-existing b", "pre-existing c"} {
		if _, err := dest([]string{"add", title}); err != nil {
			t.Fatalf("pre-seed %q: %v", title, err)
		}
	}

	out, err := dest([]string{"add", "--format", "json", exported})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(out, "Imported 5 events") {
		t.Errorf("import output = %q, want 'Imported 5 events'", out)
	}

	// Compare the two trees by title, since the ids necessarily differ.
	srcTree, err := source([]string{"list", "--format", "json", "--no-pager"})
	if err != nil {
		t.Fatalf("re-export source: %v", err)
	}
	dstTree, err := dest([]string{"list", "--format", "json", "--no-pager"})
	if err != nil {
		t.Fatalf("export dest: %v", err)
	}

	want := parentsByTitle(t, srcTree)
	got := parentsByTitle(t, dstTree)
	for _, title := range []string{"pre-existing a", "pre-existing b", "pre-existing c"} {
		if got[title] != "" {
			t.Errorf("%q gained parent %q", title, got[title])
		}
		delete(got, title)
	}
	if !maps.Equal(got, want) {
		t.Errorf("imported tree = %v, want %v", got, want)
	}
}

// parentsByTitle decodes `list --format=json` output into a title -> parent
// title map, with "" for roots. Titles stand in for ids, which differ between
// any two databases.
func parentsByTitle(t *testing.T, jsonOut string) map[string]string {
	t.Helper()
	var events []struct {
		ID       int64  `json:"id"`
		ParentID *int64 `json:"parent_id"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &events); err != nil {
		t.Fatalf("decode %q: %v", jsonOut, err)
	}
	byID := make(map[int64]string, len(events))
	for _, ev := range events {
		byID[ev.ID] = ev.Title
	}
	out := make(map[string]string, len(events))
	for _, ev := range events {
		if ev.ParentID != nil {
			out[ev.Title] = byID[*ev.ParentID]
		} else {
			out[ev.Title] = ""
		}
	}
	return out
}

// TestKongDispatch_TreeVerbsReportWhatHappened walks the under-reporting cases
// through the CLI: `delete -r` used to print `Deleted event 1` after taking
// three, `event detach` claimed a detachment on an event that never had a
// parent, and `event attach` displaced a parent without naming it.
func TestKongDispatch_TreeVerbsReportWhatHappened(t *testing.T) {
	t.Parallel()
	run := newDispatcher(t)

	for _, argv := range [][]string{
		{"add", "root"},
		{"add", "child", "--parent", "1"},
		{"add", "grandchild", "--parent", "2"},
		{"add", "loner"},
	} {
		if _, err := run(argv); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
	}

	out, err := run([]string{"event", "attach", "3", "4"})
	if err != nil {
		t.Fatalf("attach grandchild to loner: %v", err)
	}
	if !strings.Contains(out, "Attached event 3 to event 4 (was event 2)") {
		t.Errorf("attach = %q, want the displaced parent named", out)
	}
	if out, err = run([]string{"event", "attach", "3", "2"}); err != nil {
		t.Fatalf("re-attach grandchild: %v", err)
	}
	if !strings.Contains(out, "(was event 4)") {
		t.Errorf("attach = %q, want the displaced parent named", out)
	}

	if out, err = run([]string{"event", "detach", "4"}); err != nil {
		t.Fatalf("detach loner: %v", err)
	}
	if !strings.Contains(out, "Event 4 has no parent; nothing to detach") {
		t.Errorf("detach on a parentless event = %q, want the no-op named", out)
	}

	if out, err = run([]string{"event", "detach", "3"}); err != nil {
		t.Fatalf("detach grandchild: %v", err)
	}
	if !strings.Contains(out, "Detached event 3 from event 2") {
		t.Errorf("detach = %q, want the cleared parent named", out)
	}

	// 1 -> 2 is what is left of the chain, so -r takes two events.
	if out, err = run([]string{"delete", "1", "-r", "-f"}); err != nil {
		t.Fatalf("delete -r: %v", err)
	}
	if !strings.Contains(out, "Deleted event 1 and its subtree (2 events)") {
		t.Errorf("delete -r = %q, want the subtree size stated", out)
	}
}
