package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func TestShowCmd_SingleEvent_Text(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, err := s.Add(context.Background(), "show me", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ShowCmd{ID: id, Format: "text"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "show me") || !strings.Contains(got, "ID:") {
		t.Errorf("output = %q, want detail text", got)
	}
}

func TestShowCmd_Subtree(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	parent, err := s.Add(context.Background(), "parent", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if _, err := s.Add(context.Background(), "child", &parent, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil); err != nil {
		t.Fatalf("Add child: %v", err)
	}

	cmd := &ShowCmd{ID: parent, Tree: true, Format: "tree"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "parent") || !strings.Contains(got, "child") {
		t.Errorf("output = %q, want both parent and child", got)
	}
}

func TestShowCmd_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &ShowCmd{ID: 9999}
	err := cmd.Run(s, io)
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}
