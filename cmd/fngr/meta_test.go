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

func TestMetaListCmd_SearchByKey(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "people", Value: "alice"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "tag"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "tag=ops") {
		t.Errorf("output = %q, want tag=ops", got)
	}
	if strings.Contains(got, "people=alice") {
		t.Errorf("output = %q, should not contain people=alice", got)
	}
}

func TestMetaListCmd_SearchByKeyValue(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for range 2 {
		if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
			{Key: "tag", Value: "ops"},
		}, nil); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	if _, err := s.Add(context.Background(), "y", nil, []parse.Meta{
		{Key: "tag", Value: "deploy"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "tag=ops"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "tag=ops") || !strings.Contains(got, "(2)") {
		t.Errorf("output = %q, want tag=ops with (2)", got)
	}
	if strings.Contains(got, "deploy") {
		t.Errorf("output = %q, should not contain deploy", got)
	}
}

func TestMetaListCmd_SearchPeopleShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "people", Value: "sarah"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "@sarah"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "people=sarah") {
		t.Errorf("output = %q, want people=sarah", out.String())
	}
}

func TestMetaListCmd_SearchTagShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "urgent"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaListCmd{Search: "#urgent"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "tag=urgent") {
		t.Errorf("output = %q, want tag=urgent", out.String())
	}
}

func TestMetaListCmd_InvalidSearch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaListCmd{Search: "bad name"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for invalid filter")
	}
}

func TestMetaRenameCmd_BadOldFormat(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaRenameCmd{Old: "bad name", New: "tag=new"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for malformed old key")
	}
}

func TestMetaRenameCmd_NoMatch(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &MetaRenameCmd{Old: "tag=missing", New: "tag=new"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "no metadata") {
		t.Errorf("error = %v, want no-metadata error", err)
	}
}

func TestMetaRenameCmd_AbortDoesNotMutate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("n\n")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaRenameCmd{Old: "tag=ops", New: "tag=new"}
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

func TestMetaRenameCmd_ConfirmAppliesOnce(t *testing.T) {
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

	cmd := &MetaRenameCmd{Old: "tag=old", New: "tag=new"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "Renamed 3 occurrence(s)") {
		t.Errorf("output = %q, want Renamed 3 occurrence(s)", out.String())
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

func TestMetaRenameCmd_AcceptsShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "wip"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaRenameCmd{Old: "#wip", New: "#done", Force: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	wipCount, _ := s.CountMeta(context.Background(), "tag", "wip")
	if wipCount != 0 {
		t.Errorf("tag=wip count = %d after rename, want 0", wipCount)
	}
	doneCount, _ := s.CountMeta(context.Background(), "tag", "done")
	if doneCount != 1 {
		t.Errorf("tag=done count = %d after rename, want 1", doneCount)
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

func TestMetaDeleteCmd_AbortDoesNotMutate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("n\n")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "keep"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaDeleteCmd{Meta: "tag=keep"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "Aborted") {
		t.Errorf("output = %q, want Aborted", out.String())
	}
	count, _ := s.CountMeta(context.Background(), "tag", "keep")
	if count != 1 {
		t.Errorf("count after abort = %d, want 1", count)
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
}

func TestMetaDeleteCmd_AcceptsShorthand(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	if _, err := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "obsolete"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &MetaDeleteCmd{Meta: "#obsolete", Force: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	count, _ := s.CountMeta(context.Background(), "tag", "obsolete")
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}
}
