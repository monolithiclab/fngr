package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func TestEventCmd_ShowText(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, err := s.Add(context.Background(), "show me", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &EventShowCmd{ID: id, Format: "text"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "show me") || !strings.Contains(got, "ID:") {
		t.Errorf("output = %q, want detail text", got)
	}
}

func TestEventCmd_ShowSubtree(t *testing.T) {
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

	cmd := &EventShowCmd{ID: parent, Tree: true, Format: "tree"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "parent") || !strings.Contains(got, "child") {
		t.Errorf("output = %q, want both parent and child", got)
	}
}

func TestEventCmd_ShowNotFound(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &EventShowCmd{ID: 9999}
	err := cmd.Run(s, io)
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestEventCmd_TextRequiresNonEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventTextCmd{ID: id, Body: ""}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("err = %v, want empty-text error", err)
	}
}

func TestEventCmd_TextSyncs(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, _ := s.Add(context.Background(), "first @alice", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "people", Value: "alice"},
	}, nil)

	cmd := &EventTextCmd{ID: id, Body: "second @bob"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Updated event") {
		t.Errorf("output = %q, want Updated event", out.String())
	}

	ev, _ := s.Get(context.Background(), id)
	want := map[parse.Meta]bool{
		{Key: "author", Value: "alice"}: true,
		{Key: "people", Value: "bob"}:   true,
	}
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v", m)
		}
	}
}

func TestEventCmd_TimePreservesDate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	orig := time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC)
	id, _ := s.Add(context.Background(), "x", nil, nil, &orig)

	cmd := &EventTimeCmd{ID: id, Value: "09:30"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), id)
	got := ev.CreatedAt.Local()
	if got.Year() != 2026 || got.Month() != time.April || got.Day() != 15 {
		t.Errorf("date drifted: %v", got)
	}
	if got.Hour() != 9 || got.Minute() != 30 {
		t.Errorf("clock = %d:%02d, want 09:30", got.Hour(), got.Minute())
	}
}

func TestEventCmd_TimeRejectsDateOnly(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventTimeCmd{ID: id, Value: "2026-04-15"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "date-only") {
		t.Errorf("err = %v, want date-only rejection", err)
	}
}

func TestEventCmd_DatePreservesTime(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	orig := time.Date(2026, 4, 15, 14, 30, 0, 0, time.Local)
	id, _ := s.Add(context.Background(), "x", nil, nil, &orig)

	cmd := &EventDateCmd{ID: id, Value: "2026-05-01"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), id)
	got := ev.CreatedAt.Local()
	if got.Year() != 2026 || got.Month() != time.May || got.Day() != 1 {
		t.Errorf("date wrong: %v", got)
	}
	if got.Hour() != 14 || got.Minute() != 30 {
		t.Errorf("clock drifted: %d:%02d, want 14:30", got.Hour(), got.Minute())
	}
}

func TestEventCmd_DateRejectsTimeOnly(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventDateCmd{ID: id, Value: "09:30"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "time-only") {
		t.Errorf("err = %v, want time-only rejection", err)
	}
}

func TestEventCmd_AttachAndDetach(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	a, _ := s.Add(context.Background(), "a", nil, nil, nil)
	b, _ := s.Add(context.Background(), "b", nil, nil, nil)

	if err := (&EventAttachCmd{ID: b, Parent: a}).Run(s, io); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	ev, _ := s.Get(context.Background(), b)
	if ev.ParentID == nil || *ev.ParentID != a {
		t.Fatalf("ParentID = %v, want %d", ev.ParentID, a)
	}

	if err := (&EventDetachCmd{ID: b}).Run(s, io); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	ev, _ = s.Get(context.Background(), b)
	if ev.ParentID != nil {
		t.Errorf("ParentID = %d, want nil", *ev.ParentID)
	}
}

func TestEventCmd_AttachRejectsCycle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	a, _ := s.Add(context.Background(), "a", nil, nil, nil)
	b, _ := s.Add(context.Background(), "b", &a, nil, nil)

	err := (&EventAttachCmd{ID: a, Parent: b}).Run(s, io)
	if !errors.Is(err, event.ErrCycle) {
		t.Errorf("err = %v, want ErrCycle", err)
	}
}

func TestEventCmd_TagAddsAndDedups(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
	}, nil)

	cmd := &EventTagCmd{ID: id, Args: []string{"#ops", "@alice", "env=prod"}}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Tag: %v", err)
	}

	want := map[parse.Meta]bool{
		{Key: "tag", Value: "ops"}:      true,
		{Key: "people", Value: "alice"}: true,
		{Key: "env", Value: "prod"}:     true,
	}
	ev, _ := s.Get(context.Background(), id)
	if len(ev.Meta) != len(want) {
		t.Errorf("got %d meta, want %d: %v", len(ev.Meta), len(want), ev.Meta)
	}
	for _, m := range ev.Meta {
		if !want[m] {
			t.Errorf("unexpected meta %v", m)
		}
	}
}

func TestEventCmd_TagInvalidArgErrors(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventTagCmd{ID: id, Args: []string{"#ops", "bare-word", "env=prod"}}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for bare-word arg")
	}
	// Confirm no partial write happened.
	n, _ := s.CountMeta(context.Background(), "tag", "ops")
	if n != 0 {
		t.Errorf("partial write: tag=ops count = %d, want 0", n)
	}
}

func TestEventCmd_UntagRemovesAndReportsCount(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "people", Value: "alice"},
	}, nil)

	cmd := &EventUntagCmd{ID: id, Args: []string{"#ops", "@alice"}}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Untag: %v", err)
	}
	if !strings.Contains(out.String(), "Untagged event") {
		t.Errorf("output = %q, want Untagged event", out.String())
	}
	ev, _ := s.Get(context.Background(), id)
	if len(ev.Meta) != 0 {
		t.Errorf("Meta = %v, want empty", ev.Meta)
	}
}

func TestEventCmd_UntagNothingMatches(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), "x", nil, nil, nil)
	cmd := &EventUntagCmd{ID: id, Args: []string{"#ghost"}}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "nothing to untag") {
		t.Errorf("err = %v, want 'nothing to untag'", err)
	}
}
