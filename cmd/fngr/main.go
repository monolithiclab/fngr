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
func kongVars(version, username string) kong.Vars {
	return kong.Vars{
		"version":              version,
		"USER":                 username,
		"ADD_FORMATS":          strings.Join([]string{render.FormatText, render.FormatJSON}, ","),
		"ADD_FORMAT_DEFAULT":   render.FormatText,
		"LIST_FORMATS":         strings.Join(render.ListFormats, ","),
		"LIST_FORMAT_DEFAULT":  render.FormatTree,
		"EVENT_FORMATS":        strings.Join(render.EventFormats, ","),
		"EVENT_FORMAT_DEFAULT": render.FormatText,
	}
}

func main() {
	username := currentUser()

	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kongVars(version, username),
		kong.UsageOnError(),
	)

	dbPath, err := db.ResolvePath(cli.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	database, err := db.Open(dbPath, strings.HasPrefix(ctx.Command(), "add"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
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
