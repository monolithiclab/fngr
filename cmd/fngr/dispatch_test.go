package main

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
)

// dispatch parses argv, sets up the same bindings main() does, and runs the
// chosen command. Tests that go through this path catch wiring bugs (missing
// Kong bindings, wrong Run signatures, etc.) that direct cmd.Run() calls miss.
func dispatch(t *testing.T, argv []string, stdin string, isTTY bool) (string, error) {
	t.Helper()

	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("fngr"),
		kongVars("test", "tester"),
		kong.Exit(func(int) {}),
		kong.Writers(&bytes.Buffer{}, &bytes.Buffer{}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	kctx, err := parser.Parse(argv)
	if err != nil {
		return "", err
	}

	out := &bytes.Buffer{}
	kctx.BindTo(newTestStore(t), (*eventStore)(nil))
	kctx.Bind(ioStreams{
		In:    strings.NewReader(stdin),
		Out:   out,
		Err:   io.Discard,
		IsTTY: isTTY,
	})

	if err := kctx.Run(); err != nil {
		return out.String(), err
	}
	return out.String(), nil
}

// TestKongDispatch_AllCommands is the regression test for the
// "couldn't find binding of type main.eventStore" bug. Every command must be
// reachable via Kong's full Parse + Run cycle, not just by calling cmd.Run()
// in isolation.
func TestKongDispatch_AllCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		argv  []string
		stdin string
		want  string
	}{
		{name: "bare-fngr", argv: []string{}, want: ""},
		{name: "bare-fngr-reverse", argv: []string{"-r"}, want: ""},
		{name: "bare-fngr-no-pager", argv: []string{"--no-pager"}, want: ""},
		{name: "add", argv: []string{"add", "hello"}, want: "Added event 1"},
		{name: "list", argv: []string{"list"}, want: ""},
		{name: "event-bare", argv: []string{"event", "1"}, want: ""},
		{name: "event-show", argv: []string{"event", "show", "1"}, want: ""},
		{name: "event-show-tree", argv: []string{"event", "show", "1", "--tree"}, want: ""},
		{name: "event-show-json", argv: []string{"event", "show", "1", "--format", "json"}, want: ""},
		{name: "event-text", argv: []string{"event", "text", "1", "x"}, want: ""},
		{name: "event-time", argv: []string{"event", "time", "1", "09:30"}, want: ""},
		{name: "event-date", argv: []string{"event", "date", "1", "2026-05-01"}, want: ""},
		{name: "event-attach", argv: []string{"event", "attach", "1", "2"}, want: ""},
		{name: "event-detach", argv: []string{"event", "detach", "1"}, want: ""},
		{name: "event-tag", argv: []string{"event", "tag", "1", "#ops"}, want: ""},
		{name: "event-untag", argv: []string{"event", "untag", "1", "#ops"}, want: ""},
		{name: "delete", argv: []string{"delete", "1", "-f"}, want: ""},
		{name: "meta", argv: []string{"meta"}, want: ""},
		{name: "meta-search-key", argv: []string{"meta", "-S", "tag"}, want: ""},
		{name: "meta-search-keyvalue", argv: []string{"meta", "-S", "tag=a"}, want: ""},
		{name: "meta-search-shorthand", argv: []string{"meta", "-S", "#a"}, want: ""},
		{name: "meta-rename", argv: []string{"meta", "rename", "tag=a", "tag=b", "-f"}, want: ""},
		{name: "meta-delete", argv: []string{"meta", "delete", "tag=a", "-f"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := dispatch(t, tc.argv, tc.stdin, true)
			if err != nil && strings.Contains(err.Error(), "couldn't find binding") {
				t.Fatalf("kong binding error for %q: %v", tc.argv, err)
			}
			// Other errors (e.g. "no metadata matching") are fine; this test
			// only guards the wiring contract.
		})
	}
}

// TestKongDispatch_AddThenListEndToEnd exercises the full happy path through
// Kong twice against the same store, proving the dispatch + bindings handle
// stateful flows correctly.
func TestKongDispatch_AddThenListEndToEnd(t *testing.T) {
	t.Parallel()

	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("fngr"),
		kongVars("test", "tester"),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	store := newTestStore(t)
	run := func(argv []string) (string, error) {
		kctx, err := parser.Parse(argv)
		if err != nil {
			return "", err
		}
		out := &bytes.Buffer{}
		kctx.BindTo(store, (*eventStore)(nil))
		kctx.Bind(ioStreams{
			In:    strings.NewReader(""),
			Out:   out,
			Err:   io.Discard,
			IsTTY: true,
		})
		err = kctx.Run()
		return out.String(), err
	}

	if _, err := run([]string{"add", "first"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	out, err := run([]string{"list", "--format", "flat"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "first") {
		t.Errorf("list output missing 'first':\n%s", out)
	}
}
