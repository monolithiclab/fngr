package main

import (
	"context"
	"errors"
	"fmt"
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

	id, err := s.Add(context.Background(), event.AddInput{Title: "show me", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}})
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

	parent, err := s.Add(context.Background(), event.AddInput{Title: "parent", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}})
	if err != nil {
		t.Fatalf("Add parent: %v", err)
	}
	if _, err := s.Add(context.Background(), event.AddInput{Title: "child", ParentID: &parent, Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
	}}); err != nil {
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

	id, _ := s.Add(context.Background(), event.AddInput{Title: "x"})
	cmd := &EventTextCmd{ID: id, Text: ""}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("err = %v, want empty-text error", err)
	}
}

func TestEventCmd_TextSyncs(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{Title: "first @alice", Meta: []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "people", Value: "alice"},
	}})

	cmd := &EventTextCmd{ID: id, Text: "second @bob"}
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
	id, _ := s.Add(context.Background(), event.AddInput{Title: "x", CreatedAt: &orig})

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

	id, _ := s.Add(context.Background(), event.AddInput{Title: "x"})
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
	id, _ := s.Add(context.Background(), event.AddInput{Title: "x", CreatedAt: &orig})

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

	id, _ := s.Add(context.Background(), event.AddInput{Title: "x"})
	cmd := &EventDateCmd{ID: id, Value: "09:30"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "time-only") {
		t.Errorf("err = %v, want time-only rejection", err)
	}
}

// TestEventCmd_ClockVerbsRejectUnparseableValues covers the parse failure both
// verbs share, distinct from the component-mismatch errors above: the value is
// not a timestamp in any accepted layout.
func TestEventCmd_ClockVerbsRejectUnparseableValues(t *testing.T) {
	t.Parallel()
	// The command is built inside the subtest, which is where the id its own
	// store handed back is known.
	tests := []struct {
		name  string
		value string
		want  string
		verb  func(s eventStore, io ioStreams, id int64, value string) error
	}{
		{"time", "half past nope", "event time:", func(s eventStore, io ioStreams, id int64, v string) error {
			return (&EventTimeCmd{ID: id, Value: v}).Run(s, io)
		}},
		{"date", "the 32nd of Maytember", "event date:", func(s eventStore, io ioStreams, id int64, v string) error {
			return (&EventDateCmd{ID: id, Value: v}).Run(s, io)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			io, _ := newTestIO("")

			id, _ := s.Add(context.Background(), event.AddInput{Title: "x"})
			err := tt.verb(s, io, id, tt.value)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it prefixed %q", err, tt.want)
			}
		})
	}
}

func TestEventCmd_AttachAndDetach(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	a, _ := s.Add(context.Background(), event.AddInput{Title: "a"})
	b, _ := s.Add(context.Background(), event.AddInput{Title: "b"})

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
	if want := fmt.Sprintf("Detached event %d from event %d", b, a); !strings.Contains(out.String(), want) {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	// A first attach displaces nothing, so it must not claim to.
	if strings.Contains(out.String(), "(was event") {
		t.Errorf("output = %q, want no displaced parent named", out.String())
	}
}

// TestEventAttachCmd_NamesTheDisplacedParent is the attach-side half of the
// detach fix: re-attaching an event silently drops its old parent, and
// `Attached event 3 to event 1` is true of the row and says nothing about what
// it was moved away from.
func TestEventAttachCmd_NamesTheDisplacedParent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")
	ctx := context.Background()

	a, _ := s.Add(ctx, event.AddInput{Title: "a"})
	b, _ := s.Add(ctx, event.AddInput{Title: "b"})
	c, err := s.Add(ctx, event.AddInput{Title: "c", ParentID: &a})
	if err != nil {
		t.Fatalf("Add c: %v", err)
	}

	if err := (&EventAttachCmd{ID: c, Parent: b}).Run(s, io); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	want := fmt.Sprintf("Attached event %d to event %d (was event %d)", c, b, a)
	if got := out.String(); !strings.Contains(got, want) {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// TestEventAttachCmd_ReattachToTheSameParentNamesNothing keeps the suffix
// meaning "this moved": re-running the same attach displaces nothing, and
// `(was event 1)` beside `to event 1` would be noise dressed as a warning.
func TestEventAttachCmd_ReattachToTheSameParentNamesNothing(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")
	ctx := context.Background()

	a, _ := s.Add(ctx, event.AddInput{Title: "a"})
	b, err := s.Add(ctx, event.AddInput{Title: "b", ParentID: &a})
	if err != nil {
		t.Fatalf("Add b: %v", err)
	}

	if err := (&EventAttachCmd{ID: b, Parent: a}).Run(s, io); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if got := out.String(); strings.Contains(got, "(was event") {
		t.Errorf("output = %q, want no displaced parent named", got)
	}
}

// TestEventAttachCmd_UnknownEvent keeps the not-found error on the verb now
// that the existence check happens in Get rather than only in Reparent.
func TestEventAttachCmd_UnknownEvent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	a, _ := s.Add(context.Background(), event.AddInput{Title: "a"})
	err := (&EventAttachCmd{ID: 9999, Parent: a}).Run(s, io)
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestEventDetachCmd_NoParentIsAnExplicitNoop pins the low-severity report:
// detaching a parentless event printed `Detached event 1` at exit 0 with
// nothing done, which reads as confirmation that work occurred.
func TestEventDetachCmd_NoParentIsAnExplicitNoop(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{Title: "rootless"})

	if err := (&EventDetachCmd{ID: id}).Run(s, io); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "has no parent; nothing to detach") {
		t.Errorf("output = %q, want the no-op reported", got)
	}
	if strings.Contains(got, "Detached") {
		t.Errorf("output = %q, want no claim that anything was detached", got)
	}
}

// TestEventDetachCmd_UnknownEvent keeps the read-first rewrite honest: the
// existence check moved from Reparent to Get, and must still fail.
func TestEventDetachCmd_UnknownEvent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	err := (&EventDetachCmd{ID: 999}).Run(s, io)
	if !errors.Is(err, event.ErrNotFound) {
		t.Errorf("Detach = %v, want ErrNotFound", err)
	}
}

func TestEventCmd_AttachRejectsCycle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	a, _ := s.Add(context.Background(), event.AddInput{Title: "a"})
	b, _ := s.Add(context.Background(), event.AddInput{Title: "b", ParentID: &a})

	err := (&EventAttachCmd{ID: a, Parent: b}).Run(s, io)
	if !errors.Is(err, event.ErrCycle) {
		t.Errorf("err = %v, want ErrCycle", err)
	}
}

func TestEventCmd_TagAddsAndDedups(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
	}})

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

func TestEventCmd_TagMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		seed     []parse.Meta
		args     []string
		contains []string
	}{
		{
			name:     "all-new",
			seed:     nil,
			args:     []string{"#ops", "@alice"},
			contains: []string{"2 added"},
		},
		{
			name:     "all-already-present",
			seed:     []parse.Meta{{Key: "tag", Value: "ops"}, {Key: "people", Value: "alice"}},
			args:     []string{"#ops", "@alice"},
			contains: []string{"already tagged"},
		},
		{
			name:     "partial-dedup",
			seed:     []parse.Meta{{Key: "tag", Value: "ops"}},
			args:     []string{"#ops", "env=prod"},
			contains: []string{"1 added", "1 already present"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newTestStore(t)
			io, out := newTestIO("")

			id, _ := s.Add(context.Background(), event.AddInput{Title: "x", Meta: tc.seed})
			cmd := &EventTagCmd{ID: id, Args: tc.args}
			if err := cmd.Run(s, io); err != nil {
				t.Fatalf("Tag: %v", err)
			}
			got := out.String()
			for _, want := range tc.contains {
				if !strings.Contains(got, want) {
					t.Errorf("output = %q, want substring %q", got, want)
				}
			}
		})
	}
}

func TestEventCmd_TagInvalidArgErrors(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{Title: "x"})
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

	id, _ := s.Add(context.Background(), event.AddInput{Title: "x", Meta: []parse.Meta{
		{Key: "tag", Value: "ops"},
		{Key: "people", Value: "alice"},
	}})

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

	id, _ := s.Add(context.Background(), event.AddInput{Title: "x"})
	cmd := &EventUntagCmd{ID: id, Args: []string{"#ghost"}}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "nothing to untag") {
		t.Errorf("err = %v, want 'nothing to untag'", err)
	}
}

func TestEventCmd_TitleVerb(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, err := s.Add(context.Background(), event.AddInput{
		Title: "old", Body: "body stays",
		Meta: []parse.Meta{{Key: "author", Value: "alice"}},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &EventTitleCmd{ID: id, Title: "new title"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "new title" || ev.Body != "body stays" {
		t.Errorf("got (title=%q, body=%q), want (new title, body stays)", ev.Title, ev.Body)
	}
}

func TestEventCmd_TitleRejectsEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "x",
		Meta:  []parse.Meta{{Key: "author", Value: "alice"}},
	})
	cmd := &EventTitleCmd{ID: id, Title: ""}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}

func TestEventCmd_BodyVerb(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "title stays", Body: "old",
		Meta: []parse.Meta{{Key: "author", Value: "alice"}},
	})

	cmd := &EventBodyCmd{ID: id, Body: "new body"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "title stays" || ev.Body != "new body" {
		t.Errorf("got (title=%q, body=%q), want (title stays, new body)", ev.Title, ev.Body)
	}
}

func TestEventCmd_BodyAcceptsEmpty(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "stays", Body: "to clear",
		Meta: []parse.Meta{{Key: "author", Value: "alice"}},
	})

	cmd := &EventBodyCmd{ID: id, Body: ""}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "stays" || ev.Body != "" {
		t.Errorf("got (title=%q, body=%q), want (stays, '')", ev.Title, ev.Body)
	}
}

func TestEventCmd_TextVerbResplits(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "old", Body: "old body",
		Meta: []parse.Meta{{Key: "author", Value: "alice"}},
	})

	cmd := &EventTextCmd{ID: id, Text: "fresh title. fresh body"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), id)
	if ev.Title != "fresh title" || ev.Body != "fresh body" {
		t.Errorf("got (title=%q, body=%q), want (fresh title, fresh body)", ev.Title, ev.Body)
	}
}

func TestEventCmd_TextVerbRejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	id, _ := s.Add(context.Background(), event.AddInput{
		Title: "x",
		Meta:  []parse.Meta{{Key: "author", Value: "alice"}},
	})
	cmd := &EventTextCmd{ID: id, Text: ". body only"}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}
