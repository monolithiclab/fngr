package main

import (
	"os"
	"strings"
)

// envCommand returns the argv named by the first of names that is set to
// something other than whitespace, split on spaces, or nil when none is.
//
// Shared by $PAGER and $VISUAL/$EDITOR because the two used to disagree:
// pagerCommand split on spaces and the editor launcher did not, so
// `PAGER="less -R"` worked while `EDITOR="code -w"` failed with
// `fork/exec .../code -w: no such file or directory` — the whole value taken
// as one filename. Both are the same kind of value and now go through one
// function rather than two conventions.
//
// Splitting is on whitespace only: quoting and shell metacharacters are not
// interpreted, so `EDITOR='emacsclient -a ""'` is three arguments, the last
// two literal quote marks. Honouring the quotes means a shell, and handing a
// shell an environment variable is a larger decision than this one.
func envCommand(names ...string) []string {
	for _, name := range names {
		if fields := strings.Fields(os.Getenv(name)); len(fields) > 0 {
			return fields
		}
	}
	return nil
}
