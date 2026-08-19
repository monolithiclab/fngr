package main

import (
	"bytes"
	"errors"
	"io"
	"os/user"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal/db"
)

// runResult is what a run() test looks at: the status and the two streams,
// kept apart because which stream a message lands on is half of what these
// tests exist to pin.
type runResult struct {
	status int
	stdout string
	stderr string
	exits  []int // statuses Kong asked for mid-parse (--help, --version)
}

// runCLI drives the real main() body. Every caller that reaches a database
// must pass --db: db.ResolvePath falls back to ~/.fngr.db, and a test is not
// allowed anywhere near the developer's journal.
func runCLI(t *testing.T, args ...string) runResult {
	t.Helper()
	streams, stdout, stderr := newTestIOFull("", false)
	res := runResult{}
	res.status = run(args, streams, func(code int) { res.exits = append(res.exits, code) })
	res.stdout, res.stderr = stdout.String(), stderr.String()
	return res
}

func TestExitCode(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		kong int
		want int
	}{
		{"help and version pass through", 0, exitOK},
		// The only statuses Kong can pass are the two zeroes above, fngr owning
		// Parse and never calling FatalIfErrorf. The clamp is what keeps a new
		// Kong exit site from choosing fngr's status for it.
		{"anything else is an error fngr owns", 80, exitError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCode(tt.kong); got != tt.want {
				t.Errorf("exitCode(%d) = %d, want %d", tt.kong, got, tt.want)
			}
		})
	}
}

// TestRun covers what an invocation writes, where it writes it, and what it
// exits with — the three things owning kong.New/Parse was for.
//
// The stream is the regression behind most of these rows: Kong's FatalIfErrorf
// writes usage to stdout, so `fngr --bogus >/dev/null` showed a bare error and
// `fngr --bogus | cat` mixed usage into the data. A row that wants nothing on
// stdout says so by wanting nothing there — the empty case is checked, not
// skipped.
func TestRun(t *testing.T) {
	t.Parallel()

	// A path under this test's own directory, never created: `list` must
	// diagnose the absence rather than resolve $HOME behind it.
	missing := tempDB(t)
	// A path whose parent does not exist either, so nothing can open it.
	unopenable := tempDB(t) + "/nope/x.db"

	for _, tt := range []struct {
		name       string
		args       []string
		want       int
		wantStdout []string
		wantStderr []string
		notStderr  []string
		maxLines   int // 0 for no bound
	}{{
		name:       "a rejected flag gets a short usage on stderr",
		args:       []string{"--bogus"},
		want:       exitUsage,
		wantStderr: []string{"unknown flag --bogus", "Usage: fngr"},
		// With the full usage the whole command list came first and the
		// message landed 25 lines below the fold.
		maxLines: 5,
	}, {
		name:       "usage names the selected command once there is one",
		args:       []string{"event", "show", "--bogus"},
		want:       exitUsage,
		wantStderr: []string{"Usage: fngr event show", `Run "fngr event show --help"`},
		notStderr:  []string{"no command"},
	}, {
		// `list` is default:"withargs", so Kong sees no bad command at all —
		// it re-parses the word as a stray positional to list and answers with
		// `unexpected argument evnt`, naming neither the typo nor the commands
		// that exist.
		name:       "a mistyped command names the alternatives",
		args:       []string{"evnt", "5"},
		want:       exitUsage,
		wantStderr: []string{`no command "evnt"`, "event", "meta"},
		notStderr:  []string{"unexpected argument"},
	}, {
		name:       "a flag before the typo does not hide it",
		args:       []string{"--db", missing, "evnt", "5"},
		want:       exitUsage,
		wantStderr: []string{`no command "evnt"`},
		notStderr:  []string{"unexpected argument"},
	}, {
		// The value of a flag the root node does not declare — -S belongs to
		// list, spliced in at trace time — was read as a bare word, so this
		// answered `no command "ops"` and swallowed the real complaint.
		name:       "the value of a list flag is not a mistyped command",
		args:       []string{"-S", "ops", "--bogus"},
		want:       exitUsage,
		wantStderr: []string{"unknown flag --bogus", "Usage: fngr"},
		notStderr:  []string{"no command"},
	}, {
		name:       "nor is the value of a numeric one",
		args:       []string{"-n", "20", "--bogus"},
		want:       exitUsage,
		wantStderr: []string{"unknown flag --bogus"},
		notStderr:  []string{"no command"},
	}, {
		// list was named, so its stray positional is Kong's to explain; the
		// word is not a typo for a command of fngr.
		name:       "a stray argument to a named command stays Kong's",
		args:       []string{"list", "extra"},
		want:       exitUsage,
		wantStderr: []string{"unexpected argument extra"},
		notStderr:  []string{"no command"},
	}, {
		// `event` takes an <id> through its own default sub-command, so the
		// word could be an argument and deciding which is Kong's job.
		name:       "a word where an id is expected stays Kong's",
		args:       []string{"event", "shwo", "3"},
		want:       exitUsage,
		wantStderr: []string{"expected a valid 64 bit int"},
		notStderr:  []string{"no command"},
	}, {
		// help declares no eventStore, so nothing resolves a path and the
		// broken --db is never looked at. That is the user most likely to be
		// reaching for it.
		name:       "help answers with a database that cannot be opened",
		args:       []string{"--db", unopenable, "help", "add"},
		want:       exitOK,
		wantStdout: []string{"Add an event"},
	}, {
		// fail()'s prefix: db.Open's message used to go out as a bare
		// `error: …` while everything routed through Kong said `fngr: error:`.
		name:       "a missing database is a diagnosed error",
		args:       []string{"--db", missing, "list"},
		want:       exitError,
		wantStderr: []string{"fngr: error: database not found"},
		notStderr:  []string{"Usage:"},
	}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			res := runCLI(t, tt.args...)

			if res.status != tt.want {
				t.Errorf("status = %d, want %d; stderr = %q", res.status, tt.want, res.stderr)
			}
			for _, want := range tt.wantStdout {
				if !strings.Contains(res.stdout, want) {
					t.Errorf("stdout = %q, want it to contain %q", res.stdout, want)
				}
			}
			if len(tt.wantStdout) == 0 && res.stdout != "" {
				t.Errorf("stdout = %q, want nothing there", res.stdout)
			}
			for _, want := range tt.wantStderr {
				if !strings.Contains(res.stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", res.stderr, want)
				}
			}
			for _, unwanted := range tt.notStderr {
				if strings.Contains(res.stderr, unwanted) {
					t.Errorf("stderr = %q, want it not to contain %q", res.stderr, unwanted)
				}
			}
			if tt.maxLines > 0 {
				if got := strings.Count(res.stderr, "\n"); got > tt.maxLines {
					t.Errorf("wrote %d lines, want at most %d:\n%s", got, tt.maxLines, res.stderr)
				}
			}
		})
	}
}

// TestRun_AddCreatesTheDatabase is the one command allowed to, and the one
// test that takes an invocation all the way through Parse, bind, Run and close.
func TestRun_AddCreatesTheDatabase(t *testing.T) {
	t.Parallel()

	path := tempDB(t)
	res := runCLI(t, "--db", path, "add", "--author", "tester", "first note")
	if res.status != exitOK {
		t.Fatalf("add: status = %d, stderr = %q", res.status, res.stderr)
	}

	listed := runCLI(t, "--db", path, "list", "--format=flat")
	if listed.status != exitOK {
		t.Fatalf("list: status = %d, stderr = %q", listed.status, listed.stderr)
	}
	if !strings.Contains(listed.stdout, "first note") {
		t.Errorf("stdout = %q, want the event just added", listed.stdout)
	}
}

// TestRun_CommandErrorIsExitOne guards the far side of the closed set: a
// command that ran and failed is 1, whatever the failure was. This is the case
// that used to leak an $EDITOR's own status out through kong.ExitCoder.
func TestRun_CommandErrorIsExitOne(t *testing.T) {
	t.Parallel()

	// Created directly rather than through `fngr add`: the test needs a
	// migrated file to exist, not an event in it.
	path := tempDB(t)
	database, err := db.Open(path, true)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	res := runCLI(t, "--db", path, "event", "show", "999")
	if res.status != exitError {
		t.Errorf("status = %d, want %d", res.status, exitError)
	}
	if !strings.Contains(res.stderr, "fngr: error:") {
		t.Errorf("stderr = %q, want a diagnosed error", res.stderr)
	}
}

// TestRun_HelpFlagExitsZero is the other side of the exit mapping: --help is a
// request that was answered, and Kong signals it through the exit function
// mid-parse rather than by returning.
func TestRun_HelpFlagExitsZero(t *testing.T) {
	t.Parallel()

	res := runCLI(t, "--help")

	if len(res.exits) == 0 || res.exits[0] != exitOK {
		t.Errorf("Kong asked to exit %v, want [0]", res.exits)
	}
	if !strings.Contains(res.stdout, "Commands:") {
		t.Errorf("stdout = %q, want the command list", res.stdout)
	}
}

// TestRun_FlushesBufferedOutput is what moving the buffer out of withPager
// costs and buys in one test: every command's output now goes through it, so
// run is the one place that empties it. Nothing here writes 16 KiB, which is
// exactly the point — before the flush the listing exists only in memory.
func TestRun_FlushesBufferedOutput(t *testing.T) {
	t.Parallel()

	path := tempDB(t)
	if res := runCLI(t, "--db", path, "add", "--author", "tester", "first note"); res.status != exitOK {
		t.Fatalf("add: status = %d, stderr = %q", res.status, res.stderr)
	}

	streams, _, dest, _ := newTestIOBuffered("")
	// event show, not list: list is the one command that already had a flush of
	// its own, so it cannot show that the hoist reached anything else.
	if status := run([]string{"--db", path, "event", "show", "1"}, streams, func(int) {}); status != exitOK {
		t.Fatalf("status = %d, want %d", status, exitOK)
	}
	if !strings.Contains(dest.String(), "first note") {
		t.Errorf("stdout = %q, want the event run buffered and then flushed", dest.String())
	}
}

// TestRun_ReportsAFailedFlush is the cost side. The tail of any command's
// output is written at that flush and nowhere else, so swallowing its error
// would exit 0 over output the user never received.
func TestRun_ReportsAFailedFlush(t *testing.T) {
	t.Parallel()

	path := tempDB(t)
	if res := runCLI(t, "--db", path, "add", "--author", "tester", "first note"); res.status != exitOK {
		t.Fatalf("add: status = %d, stderr = %q", res.status, res.stderr)
	}

	var stderr bytes.Buffer
	streams := ioStreams{
		In:  strings.NewReader(""),
		Out: newBufferedOut(errWriter{err: errors.New("disk full")}),
		Err: &stderr,
	}
	if status := run([]string{"--db", path, "event", "show", "1"}, streams, func(int) {}); status != exitError {
		t.Errorf("status = %d, want %d", status, exitError)
	}
	if !strings.Contains(stderr.String(), "disk full") {
		t.Errorf("stderr = %q, want it to name the failed flush", stderr.String())
	}
}

// TestRun_FlushesBeforeExitingMidParse covers the one exit that does not return
// through run at all. Kong's --help and --version hooks call the exit function
// from inside Parse, so with Out buffered `fngr --help` printed nothing.
func TestRun_FlushesBeforeExitingMidParse(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--help", "--version"} {
		t.Run(flag, func(t *testing.T) {
			t.Parallel()
			var atExit string
			streams, _, dest, _ := newTestIOBuffered("")

			run([]string{flag}, streams, func(int) { atExit = dest.String() })

			if atExit == "" {
				t.Error("stdout was still empty when Kong asked to exit; the buffer was never flushed")
			}
		})
	}
}

// TestRun_ReportsAFailedFlushOnTheMidParseExit is the other half of that exit.
// run never regains control there, so the status handed to Kong's exit function
// is the only place left to say that the help text reached nothing.
func TestRun_ReportsAFailedFlushOnTheMidParseExit(t *testing.T) {
	t.Parallel()

	var got []int
	streams := ioStreams{
		In:  strings.NewReader(""),
		Out: newBufferedOut(errWriter{err: errors.New("disk full")}),
		Err: io.Discard,
	}

	run([]string{"--help"}, streams, func(status int) { got = append(got, status) })

	if len(got) != 1 || got[0] != exitError {
		t.Errorf("Kong asked to exit %v, want [%d] — the help text was never written", got, exitError)
	}
}

// TestReportParseError_NonParseErrorIsDiagnosed covers the guard that asking
// by type buys. Parse returns something other than a *kong.ParseError when a
// hook or Validate rejects an argv Kong had already understood — nothing fngr
// declares does that today, but such an error carries no Context, and both
// unplaced and writeShortUsage would dereference a nil one.
func TestReportParseError_NonParseErrorIsDiagnosed(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	parser := newTestParser(t, &bytes.Buffer{}, &stderr, nil)

	got := reportParseError(parser, errors.New("a hook said no"))

	if got != exitError {
		t.Errorf("status = %d, want %d — nothing failed to parse", got, exitError)
	}
	if !strings.Contains(stderr.String(), "a hook said no") {
		t.Errorf("stderr = %q, want the error", stderr.String())
	}
	if strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr = %q, want no usage for an argv Kong understood", stderr.String())
	}
}

func TestUnplaced(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name      string
		args      []string
		wantNode  string
		wantWords []string
	}{
		{"a mistyped command belongs to the root", []string{"evnt", "5"}, "fngr", []string{"evnt", "5"}},
		{"a value flag is Kong's to strip", []string{"--db", "notes.db", "evnt"}, "fngr", []string{"evnt"}},
		{"an inline value likewise", []string{"--db=notes.db", "evnt"}, "fngr", []string{"evnt"}},
		// The rows a second argv scanner got wrong: these flags are declared
		// on list and spliced in at trace time, so the root node has never
		// heard of them and their values read as bare words.
		{"so is the value of a list flag", []string{"-S", "ops", "--bogus"}, "fngr", nil},
		{"and of a numeric one", []string{"-n", "20", "--bogus"}, "fngr", nil},
		{"a named command keeps its own strays", []string{"list", "extra"}, "fngr list", []string{"extra"}},
		{"an implicit default defers to its parent", []string{"event", "shwo", "3"}, "fngr event", []string{"shwo", "3"}},
		// Named explicitly, so `show` keeps the node — and the scan stops at
		// the flag regardless, a flag being nobody's mistyped command.
		{"scanning stops at the first flag", []string{"event", "show", "--bogus"}, "fngr event show", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parser := newTestParser(t, &bytes.Buffer{}, &bytes.Buffer{}, nil)
			_, err := parser.Parse(tt.args)
			var parseErr *kong.ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("parse %q: want a *kong.ParseError, got %v", tt.args, err)
			}

			node, words := unplaced(parseErr.Context)

			if node == nil || node.FullPath() != tt.wantNode {
				t.Errorf("node = %v, want %q", node, tt.wantNode)
			}
			if !slices.Equal(words, tt.wantWords) {
				t.Errorf("words = %q, want %q", words, tt.wantWords)
			}
		})
	}
}

// TestUnplaced_NothingTraced covers the guard. Kong hands back a context with
// an empty path only when nothing was traced, which the real grammar cannot
// produce (list is the default command) — so it is built by hand here.
func TestUnplaced_NothingTraced(t *testing.T) {
	t.Parallel()

	node, words := unplaced(&kong.Context{})
	if node != nil || words != nil {
		t.Errorf("unplaced(empty) = %v, %q, want nil, nil", node, words)
	}
}

func TestCreatesDB(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
		want bool
	}{
		{"add creates", []string{"add", "hello"}, true},
		{"list does not", []string{"list"}, false},
		{"the default command does not", nil, false},
		{"help does not", []string{"help", "add"}, false},
		// The leaf is what declares, so a sub-command is asked itself rather
		// than inheriting an answer from anywhere above it.
		{"a nested verb does not", []string{"event", "show", "1"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parser := newTestParser(t, &bytes.Buffer{}, &bytes.Buffer{}, nil)
			ctx, err := parser.Parse(tt.args)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.args, err)
			}
			if got := createsDB(ctx); got != tt.want {
				t.Errorf("createsDB(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestCreatesDB_NothingSelected covers the guard, for the same reason
// TestUnplaced_NothingTraced does.
func TestCreatesDB_NothingSelected(t *testing.T) {
	t.Parallel()

	if createsDB(&kong.Context{}) {
		t.Error("createsDB(empty) = true, want false — nothing selected creates nothing")
	}
}

func TestCurrentUser_PrefersTheEnvironment(t *testing.T) {
	t.Setenv("USER", "from-env")
	if got := currentUser(); got != "from-env" {
		t.Errorf("currentUser() = %q, want %q", got, "from-env")
	}
}

// TestCurrentUser_FallsBackWhenUSERIsUnset pins the fallback against os/user
// itself rather than against a literal: the account the test runs as is the
// host's to decide, and an empty answer is legitimate in a container with no
// passwd entry. Both branches of the fallback are covered — whichever one this
// host takes, the expectation is derived the same way.
func TestCurrentUser_FallsBackWhenUSERIsUnset(t *testing.T) {
	t.Setenv("USER", "")
	want := ""
	if u, err := user.Current(); err == nil {
		want = u.Username
	}
	if got := currentUser(); got != want {
		t.Errorf("currentUser() = %q, want %q", got, want)
	}
}
