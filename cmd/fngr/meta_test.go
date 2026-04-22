package main

import (
	"context"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

func TestMetaListCmd_Empty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	cmd := &MetaListCmd{}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "No metadata") {
		t.Errorf("output = %q, want No metadata", out.String())
	}
}

func TestMetaListCmd_Format(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "tag=ops") || !strings.Contains(got, "(1)") {
		t.Errorf("output = %q, want tag=ops with count", got)
	}
}

func TestMetaUpdateCmd_BadOldFormat(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaUpdateCmd{Old: "bad", New: "tag=new"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for malformed old key")
	}
}

func TestMetaUpdateCmd_NoMatch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaUpdateCmd{Old: "tag=missing", New: "tag=new"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "no metadata") {
		t.Errorf("error = %v, want no-metadata error", err)
	}
}

func TestMetaUpdateCmd_AbortDoesNotMutate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("n\n")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaUpdateCmd{Old: "tag=ops", New: "tag=new"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	count, err := s.CountMeta(context.Background(), "tag", "ops")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if count != 1 {
		t.Errorf("tag=ops count = %d after abort, want 1", count)
	}
	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("output = %q, want Aborted", out.String())
	}
}

func TestMetaUpdateCmd_ConfirmAppliesOnce(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("y\n")

	for range 3 {
		if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
			{Key: "tag", Value: "old"},
		}, nil); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}

	cmd := &MetaUpdateCmd{Old: "tag=old", New: "tag=new"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "Updated 3 occurrence(s)") {
		t.Errorf("output = %q, want Updated 3 occurrence(s)", out.String())
	}

	oldCount, err := s.CountMeta(context.Background(), "tag", "old")
	if err != nil {
		t.Fatalf("CountMeta old: %v", err)
	}
	if oldCount != 0 {
		t.Errorf("tag=old count = %d, want 0", oldCount)
	}
	newCount, err := s.CountMeta(context.Background(), "tag", "new")
	if err != nil {
		t.Fatalf("CountMeta new: %v", err)
	}
	if newCount != 3 {
		t.Errorf("tag=new count = %d, want 3", newCount)
	}
}

func TestMetaDeleteCmd_NoMatch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaDeleteCmd{Meta: "tag=ghost"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "no metadata") {
		t.Errorf("error = %v, want no-metadata error", err)
	}
}

func TestMetaDeleteCmd_Force(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "obsolete"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaDeleteCmd{Meta: "tag=obsolete", Force: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "Deleted 1 occurrence(s)") {
		t.Errorf("output = %q, want Deleted 1 occurrence(s)", out.String())
	}

	count, err := s.CountMeta(context.Background(), "tag", "obsolete")
	if err != nil {
		t.Fatalf("CountMeta: %v", err)
	}
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}
}
