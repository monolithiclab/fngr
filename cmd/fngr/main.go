package main

import (
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/monolithiclab/fngr/internal"
)

var version = "dev"

type CLI struct {
	DB string `help:"Path to database file." env:"FNGR_DB" type:"path"`

	Add    AddCmd    `cmd:"" help:"Add an event."`
	List   ListCmd   `cmd:"" help:"List events."`
	Show   ShowCmd   `cmd:"" help:"Show a single event."`
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

	dbPath, err := internal.ResolveDBPath(cli.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	db, err := internal.OpenDB(dbPath, strings.HasPrefix(ctx.Command(), "add"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	err = ctx.Run(db)
	ctx.FatalIfErrorf(err)
}
