package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

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
	parser := newTestParser(t, &buf, &buf, nil)
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
		t.Fatalf("expected an error from `help <unknown>`, got plain output:\n%s", out)
	}
	// `list` is default:"withargs", so Kong's own answer was list's whole
	// usage block plus `unexpected argument totally-not-a-command` — no sign
	// the word was meant to be a command, and no list of the ones that are.
	for _, want := range []string{`fngr has no command "totally-not-a-command"`, "try one of:", "meta"} {
		if !strings.Contains(out, want) {
			t.Errorf("`help <unknown>` = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "unexpected argument") {
		t.Errorf("`help <unknown>` = %q, want the command-path error, not Kong's positional one", out)
	}
}

// TestCheckCommandPath covers the walk directly, including the nested and
// argument-taking nodes the end-to-end help test cannot reach without
// asserting on Kong's rendered usage text.
func TestCheckCommandPath(t *testing.T) {
	t.Parallel()
	parser := newTestParser(t, &bytes.Buffer{}, &bytes.Buffer{}, nil)
	root := parser.Model.Node

	tests := []struct {
		name string
		args []string
		want string // "" = must be accepted
	}{
		{name: "no args", args: nil},
		{name: "top-level command", args: []string{"add"}},
		{name: "nested command", args: []string{"event", "show"}},
		{name: "meta subcommand", args: []string{"meta", "rename"}},
		{
			name: "unknown top-level",
			args: []string{"bogus"},
			want: `fngr has no command "bogus"`,
		},
		{
			name: "unknown below a known command",
			args: []string{"meta", "bogus"},
			want: `fngr meta has no command "bogus"`,
		},
		{
			// `event` takes an <id> positional as well as its verbs, so a
			// segment matching neither is Kong's to explain, not ours.
			name: "argument-taking node accepts anything",
			args: []string{"event", "5"},
		},
		{
			// A leaf has no sub-commands to suggest; trailing words are its
			// own arguments.
			name: "leaf command accepts anything",
			args: []string{"add", "bogus"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkCommandPath(root, tt.args)
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("checkCommandPath(%q) = %v, want nil", tt.args, err)
			case tt.want != "" && err == nil:
				t.Errorf("checkCommandPath(%q) = nil, want %q", tt.args, tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Errorf("checkCommandPath(%q) = %q, want it to contain %q", tt.args, err, tt.want)
			}
		})
	}
}

// TestTakesArgument covers the two shapes fngr's grammar actually has, which
// the checkCommandPath cases exercise only indirectly: `add` carries its own
// positionals, while `event` carries none and gets its <id> from the
// `default:"withargs"` child. Reading only one of the two places is what made
// `fngr help event 5` report a mistyped command.
func TestTakesArgument(t *testing.T) {
	t.Parallel()
	parser := newTestParser(t, &bytes.Buffer{}, &bytes.Buffer{}, nil)
	root := parser.Model.Node

	tests := []struct {
		name string
		node *kong.Node
		want bool
	}{
		{name: "root, whose default command takes none", node: root, want: false},
		{name: "own positionals", node: commandChild(root, "add"), want: true},
		{name: "positionals on the default child", node: commandChild(root, "event"), want: true},
		{name: "sub-commands only", node: commandChild(root, "meta"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.node == nil {
				t.Fatal("node not found in the grammar")
			}
			if got := takesArgument(tt.node); got != tt.want {
				t.Errorf("takesArgument(%s) = %v, want %v", tt.node.Name, got, tt.want)
			}
		})
	}
}

// TestCommandNames pins the listing the error offers: sorted, and only real
// sub-commands — an <id> positional is not something to "try one of".
func TestCommandNames(t *testing.T) {
	t.Parallel()
	parser := newTestParser(t, &bytes.Buffer{}, &bytes.Buffer{}, nil)
	names := commandNames(parser.Model.Node)

	if !slices.IsSorted(names) {
		t.Errorf("commandNames = %v, want sorted", names)
	}
	for _, want := range []string{"add", "delete", "event", "help", "list", "meta"} {
		if !slices.Contains(names, want) {
			t.Errorf("commandNames = %v, want it to include %q", names, want)
		}
	}
}
