package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/event"
)

var version = "dev"

type CLI struct {
	DB      string           `help:"Path to database file." env:"FNGR_DB" type:"path"`
	Version kong.VersionFlag `help:"Print version and exit."`

	Add    AddCmd    `cmd:"" help:"Add an event."`
	List   ListCmd   `cmd:"" help:"List events."`
	Show   ShowCmd   `cmd:"" help:"Show a single event."`
	Edit   EditCmd   `cmd:"" help:"Edit an event's text or timestamp."`
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

func main() {
	username := currentUser()

	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kong.Vars{
			"version": version,
			"USER":    username,
		},
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

	store := event.NewStore(database)
	io := ioStreams{In: os.Stdin, Out: os.Stdout}
	err = ctx.Run(eventStore(store), io)
	ctx.FatalIfErrorf(err)
}
