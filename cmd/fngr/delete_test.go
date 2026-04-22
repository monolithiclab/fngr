package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func TestDeleteCmd_Confirm(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("y\n")

	id, err := s.Add(context.Background(), "doomed", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &DeleteCmd{ID: id}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := s.Get(context.Background(), id); !errors.Is(err, event.ErrNotFound) {
		t.Errorf("event not deleted; Get err = %v", err)
	}
	if !strings.Contains(out.String(), "Deleted event") {
		t.Errorf("output = %q, want Deleted event", out.String())
	}
}

func TestDeleteCmd_Abort(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("n\n")

	id, err := s.Add(context.Background(), "saved by abort", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &DeleteCmd{ID: id}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := s.Get(context.Background(), id); err != nil {
		t.Errorf("event was deleted despite abort: %v", err)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("output = %q, want Aborted", out.String())
	}
}

func TestDeleteCmd_Force(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, err := s.Add(context.Background(), "forced", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &DeleteCmd{ID: id, Force: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := s.Get(context.Background(), id); !errors.Is(err, event.ErrNotFound) {
		t.Errorf("event not deleted; Get err = %v", err)
	}
}

func TestDeleteCmd_HasChildrenWithoutRecursive(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("y\n")

	parent, err := s.Add(context.Background(), "parent", nil, nil, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if _, err := s.Add(context.Background(), "child", &parent, nil, nil); err != nil {
		t.Fatalf("Add child: %v", err)
	}

	cmd := &DeleteCmd{ID: parent}
	err = cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "has child events") {
		t.Errorf("error = %v, want child-events warning", err)
	}
}

func TestDeleteCmd_Recursive(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("y\n")

	parent, err := s.Add(context.Background(), "parent", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	child, err := s.Add(context.Background(), "child", &parent, nil, nil)
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}

	cmd := &DeleteCmd{ID: parent, Recursive: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, err := s.Get(context.Background(), child); !errors.Is(err, event.ErrNotFound) {
		t.Errorf("child not cascade-deleted; Get err = %v", err)
	}
}
