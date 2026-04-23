package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// newTestParser builds a kong.Parser identical to main()'s, with Compact
// help and a captured stdout/stderr writer so help output can be diffed.
func newTestParser(t *testing.T, out *bytes.Buffer) *kong.Kong {
	t.Helper()
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("fngr"),
		kong.Description("A CLI to log and track events."),
		kongVars("test", "tester"),
		kong.ConfigureHelp(kong.HelpOptions{Compact: true}),
		kong.Writers(out, out),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}
	return parser
}

// helpOutput captures the help text Kong emits for argv. Accepts BOTH
// shapes verbatim:
//   - flag path:  ["--help"], ["add", "--help"], ["event", "show", "--help"]
//   - verb path:  ["help"], ["help", "add"], ["help", "event", "show"]
//
// For the flag path, Kong's --help handler writes help DURING Parse and
// then calls Exit (neutralized in tests). The output is already in the
// buffer by the time Parse returns, so we don't call kctx.Run() — doing
// so would dispatch the resolved command (e.g. AddCmd.Run for
// `["add", "--help"]`), which would try to launch the editor and hang.
//
// For the verb path, kctx is for the `help` command and kctx.Run()
// invokes HelpCmd.Run which re-parses with --help appended.
//
// The two paths must produce byte-identical output for `help` to be a
// true alias for `--help`.
func helpOutput(t *testing.T, argv []string) string {
	t.Helper()
	var buf bytes.Buffer
	parser := newTestParser(t, &buf)
	kctx, err := parser.Parse(argv)
	if err != nil {
		return "ERR:" + err.Error()
	}
	if kctx != nil && strings.HasPrefix(kctx.Command(), "help") {
		if runErr := kctx.Run(); runErr != nil {
			return "ERR:" + runErr.Error()
		}
	}
	return buf.String()
}

func TestHelpCmd_BareEqualsTopLevel(t *testing.T) {
	t.Parallel()
	viaFlag := helpOutput(t, []string{"--help"})
	viaVerb := helpOutput(t, []string{"help"})
	if viaFlag == "" {
		t.Fatal("--help produced empty output; test setup is broken")
	}
	if viaFlag != viaVerb {
		t.Errorf("`fngr help` output differs from `fngr --help`:\n--- --help ---\n%s\n--- help ---\n%s", viaFlag, viaVerb)
	}
}

func TestHelpCmd_TargetEqualsCmdHelp(t *testing.T) {
	t.Parallel()
	viaFlag := helpOutput(t, []string{"add", "--help"})
	viaVerb := helpOutput(t, []string{"help", "add"})
	if viaFlag == "" {
		t.Fatal("`add --help` produced empty output; test setup is broken")
	}
	if viaFlag != viaVerb {
		t.Errorf("`fngr help add` output differs from `fngr add --help`:\n--- add --help ---\n%s\n--- help add ---\n%s", viaFlag, viaVerb)
	}
}

func TestHelpCmd_UnknownCommandErrors(t *testing.T) {
	t.Parallel()
	out := helpOutput(t, []string{"help", "totally-not-a-command"})
	if !strings.HasPrefix(out, "ERR:") {
		t.Errorf("expected an error from `help <unknown>`, got plain output:\n%s", out)
	}
}
