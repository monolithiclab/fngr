package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal"
)

var version = "dev"

type CLI struct {
	DB string `help:"Path to database file." env:"FNGR_DB" type:"path"`

	Add    internal.AddCmd    `cmd:"" default:"withargs" help:"Add an event (default command)."`
	List   internal.ListCmd   `cmd:"" help:"List events."`
	Show   internal.ShowCmd   `cmd:"" help:"Show a single event."`
	Delete internal.DeleteCmd `cmd:"" help:"Delete an event."`
	Meta   internal.MetaCmd   `cmd:"" help:"List all metadata keys and values."`
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kong.Vars{
			"version": version,
			"USER":    currentUser(),
		},
		kong.UsageOnError(),
	)

	dbPath, err := internal.ResolveDBPath(cli.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	db, err := internal.OpenDB(dbPath, ctx.Command() == "add")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	err = ctx.Run(db)
	ctx.FatalIfErrorf(err)
}
