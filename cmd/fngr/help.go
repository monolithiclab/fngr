package main

import (
	"slices"

	"github.com/alecthomas/kong"
)

// HelpCmd is the `fngr help [<command>...]` verb. It re-invokes Kong's
// parser with `--help` appended, so the existing context-sensitive help
// printer renders the same output as `fngr <command> --help`.
type HelpCmd struct {
	Args []string `arg:"" optional:"" help:"Command path to show help for (e.g. 'add' or 'event show'). Empty shows top-level help."`
}

// Run prepends --help to the requested args and re-parses. Kong's --help
// flag is a before-resolve hook that prints help and triggers Exit, so
// in production the second Parse never returns to user code on success.
// In tests with Exit neutralized, the flag-handler still writes help to
// the configured Writers and Parse returns; any real parse error is
// propagated.
func (c *HelpCmd) Run(realCtx *kong.Context) error {
	args := append(slices.Clone(c.Args), "--help")
	_, err := realCtx.Kong.Parse(args)
	return err
}
