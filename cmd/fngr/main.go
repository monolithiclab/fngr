package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
	"golang.org/x/term"
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
	// exitError is the status for an error fngr diagnosed and reported, and
	// exitUsage for a command line it would not parse. Success is 0, --help and
	// --version included, being successful requests for help. See "Exit codes"
	// in the README.
	//
	// 2 rather than the 80 Kong picks, because 80 is unguessable and means
	// nothing outside Kong: documenting it would document the leak rather than
	// close it. An unhandled panic also exits 2, being the Go runtime's own
	// status and not ours to choose — it is distinguishable by the `panic:` dump
	// on stderr, and it is a bug rather than a state the contract describes.
	exitError = 1
	exitUsage = 2

	// kongUsageStatus is the status Kong exits with on a parse failure. Naming
	// the literal is a shim, not a contract: the constant Kong uses is
	// unexported, and the type that would let us ask instead (*kong.ParseError,
	// which is exported) is only reachable if fngr owns the kong.New/Parse pair
	// rather than calling kong.Parse — queued with the rest of the main.go
	// restructure. TestKongOptions_ParseErrorIsShortAndExitsTwo drives a real
	// parse failure through the real parser, so a renumbering fails there rather
	// than reaching a user.
	kongUsageStatus = 80
)

// exitCode maps the status Kong is about to exit with onto fngr's contract,
// which is a closed set: 0, 1, 2 and nothing else.
//
// Mapping the rest to exitError rather than passing it through is what keeps
// that set closed. Kong's own statuses are 0, 1 and kongUsageStatus, but
// FatalIfErrorf runs every error through kong.ExitCoder first, so *any* status
// can arrive: an $EDITOR that exits 3 reaches AddCmd.Run as an *exec.ExitError,
// which carries ExitCode() and would otherwise make fngr exit 3 as well. That
// is the editor's status, not fngr's, and the run it describes is one fngr
// attempted and reported on — exitError, by the contract's own wording.
//
// Mapping the unknown to exitUsage instead (by elimination, so the number 80
// need never be named) is the version of this that was wrong: it answers "the
// command line could not be parsed, nothing was attempted" for a command that
// ran, which is a documented lie where the leak was merely undocumented.
func exitCode(kongStatus int) int {
	switch kongStatus {
	case 0:
		return 0
	case kongUsageStatus:
		return exitUsage
	default:
		return exitError
	}
}

// kongOptions is the parser configuration, in one place because the tests build
// their own parser and a difference between the two is a difference the tests
// cannot see. Callers may still append to override — Kong applies options in
// order, so a later kong.Writers wins — but exit is a parameter rather than an
// override, since a test that supplied its own would step over the very mapping
// it came to check.
//
// ShortUsageOnError, not UsageOnError: the full help is 30 lines of command
// list, which put the actual message ("unknown flag --foo") below the fold of
// every terminal. The short form is two lines and a pointer to `--help`.
func kongOptions(version, username string, exit func(int)) []kong.Option {
	return []kong.Option{
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kongVars(version, username),
		kong.ShortUsageOnError(),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Exit(func(status int) { exit(exitCode(status)) }),
	}
}

func main() {
	username := currentUser()

	var cli CLI
	ctx := kong.Parse(&cli, kongOptions(version, username, os.Exit)...)

	dbPath, err := db.ResolvePath(cli.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitError)
	}

	// Help shouldn't require a DB. Kong's --help flag exits during Parse;
	// the explicit `help` verb returns from Parse normally and reaches here.
	if strings.HasPrefix(ctx.Command(), "help") {
		ctx.FatalIfErrorf(ctx.Run())
		return
	}

	database, err := db.Open(dbPath, strings.HasPrefix(ctx.Command(), "add"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitError)
	}
	defer database.Close()

	ctx.BindTo(event.NewStore(database), (*eventStore)(nil))
	ctx.Bind(ioStreams{
		In:    os.Stdin,
		Out:   os.Stdout,
		Err:   os.Stderr,
		IsTTY: term.IsTerminal(int(os.Stdin.Fd())), // #nosec G115 -- fd is a small int, cannot overflow
	})
	ctx.FatalIfErrorf(ctx.Run())
}
