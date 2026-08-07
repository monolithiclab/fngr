package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func TestDeleteCmd_Confirm(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("y\n")

	id, err := s.Add(context.Background(), event.AddInput{Title: "doomed"})
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

	id, err := s.Add(context.Background(), event.AddInput{Title: "saved by abort"})
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

	id, err := s.Add(context.Background(), event.AddInput{Title: "forced"})
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

	parent, err := s.Add(context.Background(), event.AddInput{Title: "parent"})
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if _, err := s.Add(context.Background(), event.AddInput{Title: "child", ParentID: &parent}); err != nil {
		t.Fatalf("Add child: %v", err)
	}

	cmd := &DeleteCmd{ID: parent}
	err = cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "has child events") {
		t.Errorf("error = %v, want child-events warning", err)
	}
}

// TestDeleteCmd_Recursive covers the cascade and the wording together: the
// low-severity report was that `Deleted event 1` is true of the row and silent
// about the two events that went with it, and that the prompt named one event
// before taking three.
func TestDeleteCmd_Recursive(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("y\n")
	ctx := context.Background()

	root, err := s.Add(ctx, event.AddInput{Title: "root", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}})
	if err != nil {
		t.Fatalf("Add root: %v", err)
	}
	child, err := s.Add(ctx, event.AddInput{Title: "child", ParentID: &root})
	if err != nil {
		t.Fatalf("Add child: %v", err)
	}
	grandchild, err := s.Add(ctx, event.AddInput{Title: "grandchild", ParentID: &child})
	if err != nil {
		t.Fatalf("Add grandchild: %v", err)
	}

	cmd := &DeleteCmd{ID: root, Recursive: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, id := range []int64{child, grandchild} {
		if _, err := s.Get(ctx, id); !errors.Is(err, event.ErrNotFound) {
			t.Errorf("event %d not cascade-deleted; Get err = %v", id, err)
		}
	}

	// The count is the whole subtree, root included, and the prompt and the
	// result must agree — a prompt that promised three and a result that
	// reported one would be the same defect in a different place.
	got := out.String()
	want := fmt.Sprintf("event %d and its subtree (3 events)", root)
	if n := strings.Count(got, want); n != 2 {
		t.Errorf("output = %q, want %q in both the prompt and the result", got, want)
	}
}

// TestDeleteCmd_RecursiveOnACorruptTree checks the fallback wording. A stored
// cycle stops GetSubtree, but the delete is an FK cascade and works anyway —
// `delete -r` is one of the ways out of a corrupt tree, so it must not start
// failing on the database that needs it.
func TestDeleteCmd_RecursiveOnACorruptTree(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("y\n")
	ctx := context.Background()

	for _, title := range []string{"one", "two"} {
		if _, err := s.Add(ctx, event.AddInput{Title: title}); err != nil {
			t.Fatalf("Add %s: %v", title, err)
		}
	}
	forgeParent(t, s, 1, 2)
	forgeParent(t, s, 2, 1)

	cmd := &DeleteCmd{ID: 1, Recursive: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "event 1 and all its children") {
		t.Errorf("output = %q, want the un-counted fallback wording", got)
	}
	if strings.Contains(got, "subtree (") {
		t.Errorf("output = %q, want no count when the tree cannot be walked", got)
	}
}
