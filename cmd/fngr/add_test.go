package main

import (
	"context"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
)

func TestAddCmd_Success(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	cmd := &AddCmd{Args: []string{"hello world"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "Added event 1") {
		t.Errorf("output = %q, want contains 'Added event 1'", got)
	}

	ev, err := s.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Text != "hello world" {
		t.Errorf("event text = %q, want %q", ev.Text, "hello world")
	}
}

func TestAddCmd_RequiresAuthor(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"hi"}}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "author is required") {
		t.Errorf("error = %v, want author-required", err)
	}
}

func TestAddCmd_InvalidTime(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"hi"}, Author: "alice", Time: "not-a-time"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "invalid --time") {
		t.Errorf("error = %v, want invalid-time error", err)
	}
}

func TestAddCmd_WithMeta(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"deploy #ops"}, Author: "alice", Meta: []string{"env=prod"}}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, err := s.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	wantKeys := map[string]string{"author": "alice", "tag": "ops", "env": "prod"}
	for _, m := range ev.Meta {
		if v, ok := wantKeys[m.Key]; ok && v == m.Value {
			delete(wantKeys, m.Key)
		}
	}
	if len(wantKeys) > 0 {
		t.Errorf("missing meta entries: %v; got: %v", wantKeys, ev.Meta)
	}
}

func TestAddCmd_BadFlagMeta(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"hi"}, Author: "alice", Meta: []string{"noequals"}}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for malformed meta")
	}
}

func TestAddCmd_MultiArgJoinsWithSpace(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	cmd := &AddCmd{Args: []string{"deploy", "v1.2", "to", "staging"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Added event 1") {
		t.Errorf("output = %q, want 'Added event 1'", out.String())
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "deploy v1.2 to staging" {
		t.Errorf("text = %q, want %q", ev.Text, "deploy v1.2 to staging")
	}
}

func TestAddCmd_StdinBody(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull("piped body content", false) // isTTY=false

	cmd := &AddCmd{Author: "alice"} // no args
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Added event 1") {
		t.Errorf("output = %q, want 'Added event 1'", out.String())
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "piped body content" {
		t.Errorf("text = %q, want %q", ev.Text, "piped body content")
	}
}

func TestAddCmd_EditorBody(t *testing.T) {
	s := newTestStore(t)
	io, out, _ := newTestIOFull("", true) // isTTY=true

	origEditor := launchEditor
	launchEditor = func(initial string) (string, error) {
		if initial != "" {
			t.Errorf("editor called with non-empty initial %q", initial)
		}
		return "from editor", nil
	}
	t.Cleanup(func() { launchEditor = origEditor })

	cmd := &AddCmd{Author: "alice"} // no args, no -e — bare TTY auto-launches editor
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Added event 1") {
		t.Errorf("output = %q, want 'Added event 1'", out.String())
	}
	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "from editor" {
		t.Errorf("text = %q, want 'from editor'", ev.Text)
	}
}

func TestAddCmd_ArgsPlusEditorPrefills(t *testing.T) {
	s := newTestStore(t)
	io, _, _ := newTestIOFull("", true)

	var gotInit string
	origEditor := launchEditor
	launchEditor = func(initial string) (string, error) {
		gotInit = initial
		return initial + " z", nil
	}
	t.Cleanup(func() { launchEditor = origEditor })

	cmd := &AddCmd{Args: []string{"x", "y"}, Edit: true, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotInit != "x y" {
		t.Errorf("editor initial = %q, want %q", gotInit, "x y")
	}
	ev, _ := s.Get(context.Background(), 1)
	if ev.Text != "x y z" {
		t.Errorf("text = %q, want %q", ev.Text, "x y z")
	}
}

func TestAddCmd_EditorCancel(t *testing.T) {
	s := newTestStore(t)
	io, out, errBuf := newTestIOFull("", true)

	origEditor := launchEditor
	launchEditor = func(string) (string, error) { return "", errCancel }
	t.Cleanup(func() { launchEditor = origEditor })

	cmd := &AddCmd{Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run returned err = %v, want nil (cancel is clean exit)", err)
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	if !strings.Contains(errBuf.String(), "cancelled (empty body)") {
		t.Errorf("stderr = %q, want 'cancelled (empty body)'", errBuf.String())
	}

	// Confirm no event was created.
	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 0 {
		t.Errorf("created %d events, want 0", len(events))
	}
}

func TestAddCmd_ArgsAndStdinError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("piped", false) // isTTY=false

	cmd := &AddCmd{Args: []string{"argbody"}, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("err = %v, want 'ambiguous'", err)
	}
}

func TestAddCmd_EmptyArgRejected(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{""}, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "event text cannot be empty") {
		t.Errorf("err = %v, want 'event text cannot be empty'", err)
	}

	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 0 {
		t.Errorf("created %d events, want 0 (empty arg should reject)", len(events))
	}
}

func TestAddCmd_EditFlagPipedError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("piped", false)

	cmd := &AddCmd{Edit: true, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--edit conflicts") {
		t.Errorf("err = %v, want '--edit conflicts'", err)
	}
}
