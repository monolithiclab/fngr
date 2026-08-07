package main

import (
	"fmt"
	"slices"
	"strings"

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
	// Vet the path before handing it back to Kong — see checkCommandPath.
	if err := checkCommandPath(realCtx.Model.Node, c.Args); err != nil {
		return err
	}
	args := append(slices.Clone(c.Args), "--help")
	_, err := realCtx.Kong.Parse(args)
	return err
}

// checkCommandPath walks args down the command tree and names the
// alternatives at the first segment that matches nothing.
//
// It exists because `list` is the default-with-args command, so a misspelled
// verb is not a parse failure at all — it re-parses as a stray positional
// argument *to list*, and Kong's answer is list's entire usage block followed
// by `unexpected argument bogus`, with no sign that the word was meant to be a
// command and no list of the ones that are.
//
// It stays quiet wherever the segment could legitimately be something other
// than a command — see takesArgument. Only a node that offers sub-commands and
// takes no argument of its own can say for certain that a word is a typo.
func checkCommandPath(node *kong.Node, args []string) error {
	for _, arg := range args {
		child := commandChild(node, arg)
		if child == nil {
			names := commandNames(node)
			if len(names) == 0 || takesArgument(node) {
				return nil
			}
			return fmt.Errorf("%s has no command %q; try one of: %s",
				node.FullPath(), arg, strings.Join(names, ", "))
		}
		node = child
	}
	return nil
}

// takesArgument reports whether an unrecognised word under node could be an
// argument rather than a mistyped command — `fngr help event 5` names no
// command, but `event` does take an <id> there, and deciding which the user
// meant is Kong's job, not this one's.
//
// All three shapes count, because Kong will try all three: node's own
// positionals (which it consumes *before* looking at sub-commands), those of
// its `default:"withargs"` child (where that tag puts them, rather than on
// node), and an `arg:""` branch child.
func takesArgument(node *kong.Node) bool {
	if len(node.Positional) > 0 {
		return true
	}
	if node.DefaultCmd != nil && len(node.DefaultCmd.Positional) > 0 {
		return true
	}
	return slices.ContainsFunc(node.Children, func(child *kong.Node) bool {
		return child.Type == kong.ArgumentNode
	})
}

// commandChild finds the named sub-command of node. Hidden children match:
// this is resolution, not the candidate list, and that is Kong's own split.
// Aliases are deliberately not consulted — fngr declares none, and Kong's
// precedence (an alias loses to any real command of that name, wherever it
// sits) is not something to guess at from the outside.
func commandChild(node *kong.Node, name string) *kong.Node {
	for _, child := range node.Children {
		if child.Type == kong.CommandNode && child.Name == name {
			return child
		}
	}
	return nil
}

// commandNames lists node's sub-commands, sorted, hidden ones omitted.
func commandNames(node *kong.Node) []string {
	var names []string
	for _, child := range node.Children {
		if child.Type == kong.CommandNode && !child.Hidden {
			names = append(names, child.Name)
		}
	}
	slices.Sort(names)
	return names
}
