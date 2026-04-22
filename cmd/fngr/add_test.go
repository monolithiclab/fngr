package main

import (
	"context"
	"strings"
	"testing"
)

func TestAddCmd_Success(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	cmd := &AddCmd{Text: "hello world", Author: "alice"}
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

	cmd := &AddCmd{Text: "hi"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "author is required") {
		t.Errorf("error = %v, want author-required", err)
	}
}

func TestAddCmd_RequiresText(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "event text cannot be empty") {
		t.Errorf("error = %v, want empty-text error", err)
	}
}

func TestAddCmd_InvalidTime(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Text: "hi", Author: "alice", Time: "not-a-time"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "invalid --time") {
		t.Errorf("error = %v, want invalid-time error", err)
	}
}

func TestAddCmd_WithMeta(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Text: "deploy #ops", Author: "alice", Meta: []string{"env=prod"}}
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

	cmd := &AddCmd{Text: "hi", Author: "alice", Meta: []string{"noequals"}}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for malformed meta")
	}
}
