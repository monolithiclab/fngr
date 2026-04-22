package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
)

func TestEditCmd_RequiresChange(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &EditCmd{ID: 1}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "nothing to edit") {
		t.Errorf("error = %v, want nothing-to-edit error", err)
	}
}

func TestEditCmd_RejectsEmptyText(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	empty := ""
	cmd := &EditCmd{ID: 1, Text: &empty}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("error = %v, want empty-text error", err)
	}
}

func TestEditCmd_NotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	newText := "x"
	cmd := &EditCmd{ID: 9999, Text: &newText, Force: true}
	err := cmd.Run(s, io)
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestEditCmd_ConfirmAppliesText(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("y\n")

	id, err := s.Add(context.Background(), "before", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newText := "after"
	cmd := &EditCmd{ID: id, Text: &newText}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Updated event") {
		t.Errorf("output = %q, want Updated event", out.String())
	}

	got, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Text != newText {
		t.Errorf("text = %q, want %q", got.Text, newText)
	}
}

func TestEditCmd_AbortDoesNotMutate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("n\n")

	id, err := s.Add(context.Background(), "keep", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	newText := "discard"
	cmd := &EditCmd{ID: id, Text: &newText}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Text != "keep" {
		t.Errorf("text = %q, want unchanged 'keep'", got.Text)
	}
}

func TestEditCmd_InvalidTime(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, err := s.Add(context.Background(), "x", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &EditCmd{ID: id, Time: "not-a-time", Force: true}
	err = cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--time") {
		t.Errorf("error = %v, want --time error", err)
	}
}
