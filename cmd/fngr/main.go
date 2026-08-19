package main

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

var version = "dev"

type CLI struct {
	DB      string           `help:"Path to database file." env:"FNGR_DB" type:"path"`
	Version kong.VersionFlag `help:"Print version and exit."`

	Add    AddCmd    `cmd:"" help:"Add an event."`
	List   ListCmd   `cmd:"" default:"withargs" help:"List events (default command)."`
	Event  EventCmd  `cmd:"" help:"Show or modify a single event."`
	Delete DeleteCmd `cmd:"" help:"Delete an event."`
	Meta   MetaCmd   `cmd:"" help:"List all metadata keys and values."`
	Help   HelpCmd   `cmd:"" help:"Show help for a command."`
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// kongVars centralizes the template variables Kong tags reference. Both
// main() and the dispatch tests use this so the two call sites can't drift.
//
// Each vocabulary is one variable serving both the `enum:` tag and the `help:`
// text — Kong interpolates ${VAR} in both, and trims each comma-split enum
// value, so `", "` reads as prose in the help and parses the same as `","`.
// Spelling the accepted formats out in prose instead let the two disagree, and
// the help is the only place a user finds out `markdown` is a spelling at all.
func kongVars(version, username string) kong.Vars {
	return kong.Vars{
		"version":              version,
		"USER":                 username,
		"ADD_FORMATS":          strings.Join(render.AddFormats, ", "),
		"ADD_FORMAT_DEFAULT":   render.FormatText,
		"LIST_FORMATS":         strings.Join(render.ListFormats, ", "),
		"LIST_FORMAT_DEFAULT":  render.FormatTree,
		"EVENT_FORMATS":        strings.Join(render.EventFormats, ", "),
		"EVENT_FORMAT_DEFAULT": render.FormatText,
		"TIME_ABSOLUTE":        timefmt.AbsoluteForms,
		"TIME_RELATIVE":        timefmt.RelativeForms,
	}
}

const (
	// The whole exit vocabulary: 0 for success (--help and --version included,
	// being successful requests for help), exitError for an error fngr
	// diagnosed and reported, exitUsage for a command line it would not parse.
	// See "Exit codes" in the README.
	//
	// It is a closed set because run returns one of these three and nothing
	// else — not because anything maps onto them. The version that mapped was
	// the version that leaked: fngr called kong.Parse, so the status came from
	// Kong's FatalIfErrorf, which runs every error through kong.ExitCoder and
	// would happily exit 3 because an $EDITOR did.
	//
	// 2 rather than the 80 Kong exits with on a parse failure, because 80 is
	// unguessable and means nothing outside Kong. An unhandled panic also exits
	// 2, being the Go runtime's status and not ours to choose — it is a bug
	// rather than a state the contract describes, and the `panic:` dump on
	// stderr tells them apart.
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// exitCode clamps a status Kong picks into that vocabulary. Kong reaches the
// exit function from exactly two places now that fngr owns Parse and never
// calls FatalIfErrorf — the --help and --version hooks — and both pass 0. The
// clamp is what keeps that an observation rather than an assumption.
func exitCode(kongStatus int) int {
	if kongStatus == exitOK {
		return exitOK
	}
	return exitError
}

// helpOptions is the help configuration, named rather than inlined into
// kongOptions because writeShortUsage calls a help printer directly and must
// pass the same one — Kong keeps its copy in an unexported field.
var helpOptions = kong.HelpOptions{Compact: true}

// kongOptions is the parser configuration, in one place because the tests build
// their own parser and a difference between the two is a difference the tests
// cannot see. Callers may still append to override — Kong applies options in
// order, so a later kong.Writers wins — but exit is a parameter rather than an
// override, since a test that supplied its own would step over the very mapping
// it came to check.
//
// No kong.ShortUsageOnError: it configures FatalIfErrorf, which fngr does not
// call. writeShortUsage prints the same two lines, on stderr — see
// reportParseError.
func kongOptions(version, username string, exit func(int)) []kong.Option {
	return []kong.Option{
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kongVars(version, username),
		kong.ConfigureHelp(helpOptions),
		kong.Exit(func(status int) { exit(exitCode(status)) }),
	}
}

func main() {
	streams := ioStreams{
		In:    os.Stdin,
		Out:   newBufferedOut(os.Stdout),
		Err:   os.Stderr,
		IsTTY: isTerminalFile(os.Stdin),
	}
	os.Exit(run(os.Args[1:], streams, os.Exit))
}

// run is main's body, returning a status instead of exiting on one, so that
// `defer database.Close()` actually runs — reaching os.Exit through Kong's
// FatalIfErrorf meant it never did, leaving a WAL database's -wal and -shm
// files for the next process to recover and its checkpoint undone.
//
// It owns the kong.New/Parse pair rather than calling kong.Parse, which is what
// makes three things fngr's decision instead of Kong's: the exit status (above),
// where a message about a failed command line goes (reportParseError), and
// whether a mistyped command is diagnosed at all (checkCommandPath, which until
// now only the `help` verb reached).
//
// exit is Kong's, not run's: the --help and --version hooks call it mid-Parse
// and expect it not to return. Nothing is open yet at that point, which is why
// they can leave without unwinding.
func run(args []string, streams ioStreams, exit func(int)) int {
	var cli CLI
	out := streams.Out
	// Every byte fngr writes to stdout is held in a buffer (see output.go), so
	// every way out of run has to empty it. This defer is the one that covers
	// all of them; the two explicit flushes below are about *when*, not whether.
	defer func() { _ = flushOut(out) }()
	// Kong's --help and --version hooks call exit from inside Parse and never
	// return, so that defer is not one of the ways out: `fngr --help` would
	// exit 0 having printed nothing at all. A failed flush there is the same
	// lost output it is anywhere else, and the status is the only place left to
	// report it.
	exitFlushed := func(status int) {
		if err := flushOut(out); err != nil {
			status = exitError
		}
		exit(status)
	}
	parser, err := kong.New(&cli, append(kongOptions(version, currentUser(), exitFlushed),
		kong.Writers(streams.Out, streams.Err),
	)...)
	if err != nil {
		// Unreachable short of a malformed grammar in CLI, which is a
		// build-time mistake every test that builds a parser catches first.
		// Reported rather than panicked so that even then fngr exits inside
		// its own contract.
		fmt.Fprintf(streams.Err, "fngr: error: %v\n", err)
		return exitError
	}

	ctx, err := parser.Parse(args)
	if err != nil {
		return reportParseError(parser, err)
	}

	ctx.Bind(streams)

	// The store is bound lazily, so which commands need a database is read off
	// their own Run signatures — `fngr help` declares no eventStore, so nothing
	// resolves one, so no path is resolved and no file is opened, and it stays
	// answerable with a --db that is missing or unreadable. That used to be
	// strings.HasPrefix(ctx.Command(), "help"), true of any command whose name
	// merely starts that way: `fngr helpers` would have skipped the open and
	// then run a command with no store to run against.
	var database *sql.DB
	defer func() {
		if database != nil {
			_ = database.Close()
		}
	}()
	_ = ctx.BindToProvider(func() (eventStore, error) {
		dbPath, err := db.ResolvePath(cli.DB)
		if err != nil {
			return nil, err
		}
		opened, err := db.Open(dbPath, createsDB(ctx))
		if err != nil {
			return nil, err
		}
		database = opened
		return event.NewStore(opened), nil
	})

	// Flushed here rather than by each command, which is the whole reason the
	// buffer moved out of withPager, and *before* anything is said about the
	// result: stderr is unbuffered, so diagnosing first would print the verdict
	// above the output it is about. A failed flush is itself the command's error
	// — those bytes were written nowhere else, and exiting 0 over output the
	// user never received is the failure mode — but an error already on its way
	// out says more, so it wins. cmp.Or is that rule, and Go's left-to-right
	// argument order is what makes ctx.Run happen first and the flush happen
	// regardless.
	if err := cmp.Or(ctx.Run(), flushOut(out)); err != nil {
		return fail(parser, err)
	}
	return exitOK
}

// fail reports an error fngr diagnosed. parser.Errorf is Kong's own formatter
// — `fngr: error: <msg>`, indented across continuation lines — which is what
// every error routed through FatalIfErrorf already looked like; the two
// db.Open messages that went out as a bare `error: …` were the odd ones.
func fail(parser *kong.Kong, err error) int {
	parser.Errorf("%s", err)
	return exitError
}

// reportParseError answers a command line Kong would not parse.
//
// The short usage goes to stderr. Kong's FatalIfErrorf writes it — plus an
// unconditional blank line — to *stdout*, so `fngr --bogus >/dev/null` showed
// a bare error with no usage and `fngr --bogus | cat` mixed usage into the
// data stream.
//
// A mistyped command is diagnosed instead of relayed. `list` is
// default:"withargs", so `fngr evnt 5` is not "no such command" to Kong at all
// — it re-parses as a stray positional *to list*, and the answer was list's
// usage block plus `unexpected argument evnt`, naming neither the typo nor the
// commands that exist. checkCommandPath is the same vetting `fngr help` does;
// reaching it from here is what makes it cover `fngr <typo>` and not just
// `fngr help <typo>`.
func reportParseError(parser *kong.Kong, err error) int {
	var parseErr *kong.ParseError
	if !errors.As(err, &parseErr) {
		// Not a parse failure: a hook or Validate rejected an argv Kong had
		// already understood, which is fngr diagnosing something rather than
		// failing to read the command line. Asking by type is only possible
		// because fngr owns Parse; the alternative was recognising Kong's
		// unexported status 80 by its number.
		return fail(parser, err)
	}
	if hint := checkCommandPath(unplaced(parseErr.Context)); hint != nil {
		parser.Errorf("%s", hint)
		return exitUsage
	}
	writeShortUsage(parser, parseErr.Context)
	parser.Errorf("%s", err)
	return exitUsage
}

// unplaced reports where Kong's trace stopped and which of the words it never
// placed could name a command — the two arguments checkCommandPath wants.
//
// Both are read off the failed parse rather than re-derived from argv, because
// argv cannot be read a second time without rebuilding Kong's scanner. The
// words that could name a command are the ones left once every flag *and its
// value* is taken out, and only Kong knows which flags take a value: `list` is
// default:"withargs", so -S/-n/--format sit on the list node and are spliced in
// at trace time rather than declared on the root. A scan of the root's own
// flags therefore read their values as bare words, and `fngr -S ops --bogus`
// answered `fngr has no command "ops"`, swallowing the unknown flag that was
// the actual complaint. Path.Remainder() is Kong's own answer to the same
// question, already tokenized.
//
// Scanning stops at the first word starting with `-`: a flag is not a mistyped
// command, and whatever Kong has to say about it is the better message.
func unplaced(ctx *kong.Context) (*kong.Node, []string) {
	var node *kong.Node
	var rest, prev []string
	for i, path := range ctx.Path {
		rem := path.Remainder()
		if at := path.Node(); at != nil {
			// A default command is entered without its name being typed, so a
			// word that failed there belongs to its parent's vocabulary rather
			// than its own. Consuming nothing is what tells the two apart:
			// `fngr list extra` drops "list" from the remainder on the way in
			// and `fngr meat` does not, and only the second is a typo for a
			// command name.
			if i > 0 && at.Parent != nil && len(rem) == len(prev) {
				at = at.Parent
			}
			node = at
		}
		prev, rest = rem, rem
	}
	if node == nil {
		return nil, nil
	}
	var words []string
	for _, word := range rest {
		if strings.HasPrefix(word, "-") {
			break
		}
		words = append(words, word)
	}
	return node, words
}

// writeShortUsage prints Kong's own short usage on stderr, and the blank line
// separating it from the error that follows. The printer is Kong's — the two
// lines it writes are a format the library owns, and a private copy of them
// would have to be re-checked against every upgrade — but it writes to the
// parser's stdout, which is where FatalIfErrorf's copy went and the reason
// this function exists at all.
func writeShortUsage(parser *kong.Kong, ctx *kong.Context) {
	defer func(stdout io.Writer) { parser.Stdout = stdout }(parser.Stdout)
	parser.Stdout = parser.Stderr
	_ = kong.DefaultShortHelpPrinter(helpOptions, ctx)
	fmt.Fprintln(parser.Stderr)
}

// dbCreator is implemented by a command that may create the database rather
// than requiring one that already exists.
type dbCreator interface{ createsDB() bool }

// createsDB reports whether the selected command may create the database. It
// used to be strings.HasPrefix(ctx.Command(), "add"), true of any command whose
// name merely starts that way — `fngr addendum` would have silently started a
// second journal. Selected() is the leaf, so a declaration on `add` is found
// for `fngr add` and one on a sub-command would be found for that alone.
func createsDB(ctx *kong.Context) bool {
	node := ctx.Selected()
	if node == nil {
		return false
	}
	creator, ok := node.Target.Addr().Interface().(dbCreator)
	return ok && creator.createsDB()
}
