package main

import (
	"context"
	"strings"
	"testing"
	"time"

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
	if ev.Title != "hello world" {
		t.Errorf("event text = %q, want %q", ev.Title, "hello world")
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

func TestAddCmd_EmptyMetaKeyRejected(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"hi"}, Author: "alice", Meta: []string{"=value"}}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "empty key") {
		t.Fatalf("err = %v, want 'empty key'", err)
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
	if ev.Title != "deploy v1.2 to staging" {
		t.Errorf("text = %q, want %q", ev.Title, "deploy v1.2 to staging")
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
	if ev.Title != "piped body content" {
		t.Errorf("text = %q, want %q", ev.Title, "piped body content")
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
	if ev.Title != "from editor" {
		t.Errorf("text = %q, want 'from editor'", ev.Title)
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
	if ev.Title != "x y z" {
		t.Errorf("text = %q, want %q", ev.Title, "x y z")
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

// Regression guard: `fngr add "note"` run non-interactively (script, CI,
// cron) has stdin bound to an empty /dev/null. It must use the args, not
// trip the args+stdin ambiguity guard. Exercised through the Run path.
func TestAddCmd_ArgsNonTTYEmptyStdin(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull("", false) // isTTY=false, no piped data

	cmd := &AddCmd{Args: []string{"deploy", "done"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Added event 1") {
		t.Errorf("output = %q, want 'Added event 1'", out.String())
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Title != "deploy done" {
		t.Errorf("title = %q, want %q", ev.Title, "deploy done")
	}
}

func TestAddCmd_EmptyArgRejected(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{""}, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "event title cannot be empty") {
		t.Errorf("err = %v, want 'event title cannot be empty'", err)
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

func TestAddCmd_FormatJSON_Single(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull(`{"title":"hi"}`, false) // piped stdin

	cmd := &AddCmd{Format: "json", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 1 event") {
		t.Errorf("output = %q, want 'Imported 1 event'", out.String())
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Title != "hi" {
		t.Errorf("title = %q, want 'hi'", ev.Title)
	}
}

func TestAddCmd_FormatJSON_Array(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull(`[{"title":"a"},{"title":"b"},{"title":"c"}]`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 3 events") {
		t.Errorf("output = %q, want 'Imported 3 events'", out.String())
	}

	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 3 {
		t.Errorf("created %d events, want 3", len(events))
	}
}

func TestAddCmd_FormatJSON_EmptyArray(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull(`[]`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run returned err = %v, want nil for empty array", err)
	}
	if !strings.Contains(out.String(), "Imported 0 events") {
		t.Errorf("output = %q, want 'Imported 0 events'", out.String())
	}
}

func TestAddCmd_FormatJSON_AtomicRollback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// Second record references parent_id=9999 which doesn't exist → rollback.
	io, _, _ := newTestIOFull(`[{"title":"good"},{"title":"bad","parent_id":9999}]`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("Run returned nil err, want parent-not-found")
	}

	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 0 {
		t.Errorf("created %d events, want 0 (atomic rollback)", len(events))
	}
}

func TestAddCmd_FormatJSON_EditConflicts(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("", true)

	cmd := &AddCmd{Format: "json", Edit: true, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--edit conflicts with --format=json") {
		t.Errorf("err = %v, want '--edit conflicts with --format=json'", err)
	}
}

func TestAddCmd_FormatJSON_BareTTYRejects(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull("", true) // TTY, no args

	cmd := &AddCmd{Format: "json", Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "requires JSON via args or piped stdin") {
		t.Errorf("err = %v, want 'requires JSON via args or piped stdin'", err)
	}
}

func TestAddCmd_FormatJSON_FromArgs(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	cmd := &AddCmd{Format: "json", Args: []string{`{"title":"from arg"}`}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 1 event") {
		t.Errorf("output = %q, want 'Imported 1 event'", out.String())
	}
	ev, _ := s.Get(context.Background(), 1)
	if ev.Title != "from arg" {
		t.Errorf("title = %q, want 'from arg'", ev.Title)
	}
}

func TestAddCmd_FormatJSON_TimeFlagFallback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"title":"hi"}`, false)

	cmd := &AddCmd{Format: "json", Time: "2026-04-01", Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), 1)
	// timefmt.Parse interprets "2026-04-01" as local-tz midnight; storage
	// round-trips via UTC, so compare in the same local frame.
	got := ev.CreatedAt.Local()
	if got.Year() != 2026 || got.Month() != 4 || got.Day() != 1 {
		t.Errorf("CreatedAt (local) = %v, want 2026-04-01", got)
	}
}

func TestAddCmd_FormatJSON_MalformedJSON(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"title":`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--format=json") {
		t.Errorf("err = %v, want '--format=json' parse error", err)
	}
}

func TestAddCmd_FormatJSON_BadCLITimeFallback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"title":"hi"}`, false)

	cmd := &AddCmd{Format: "json", Time: "not-a-time", Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "invalid --time") {
		t.Errorf("err = %v, want 'invalid --time'", err)
	}
}

func TestAddCmd_FormatJSON_BadCLIMetaFallback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"title":"hi"}`, false)

	cmd := &AddCmd{Format: "json", Meta: []string{"noequals"}, Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil {
		t.Fatal("expected error for malformed --meta default")
	}
}

func TestAddCmd_FormatJSON_PerRecordError(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// Second record has empty title → jsonInputToAddInput returns an error.
	io, _, _ := newTestIOFull(`[{"title":"good"},{"title":""}]`, false)

	cmd := &AddCmd{Format: "json", Author: "alice"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "record 1: title is required") {
		t.Errorf("err = %v, want 'record 1: title is required'", err)
	}

	events, _ := s.List(context.Background(), event.ListOpts{})
	if len(events) != 0 {
		t.Errorf("created %d events, want 0 (validation aborts before AddMany)", len(events))
	}
}

func TestAddCmd_FormatJSON_AuthorFromJSONOverride(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out, _ := newTestIOFull(`{"title":"hi","meta":[["author","bob"]]}`, false)

	// Empty CLI author is OK because the JSON record names its own author.
	cmd := &AddCmd{Format: "json", Author: ""}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "Imported 1 event") {
		t.Errorf("output = %q, want 'Imported 1 event'", out.String())
	}
	ev, _ := s.Get(context.Background(), 1)
	authorCount := 0
	for _, m := range ev.Meta {
		if m.Key == "author" {
			authorCount++
			if m.Value != "bob" {
				t.Errorf("author = %q, want 'bob'", m.Value)
			}
		}
	}
	if authorCount != 1 {
		t.Errorf("found %d author entries, want exactly 1 (no ghost {author, \"\"})", authorCount)
	}
}

func TestAddCmd_FormatJSON_NoAuthorAnywhereRejects(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"title":"hi"}`, false)

	cmd := &AddCmd{Format: "json", Author: ""}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "author is required") {
		t.Errorf("err = %v, want 'author is required'", err)
	}
}

func TestAddCmd_FormatJSON_MetaFlagFallback(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _, _ := newTestIOFull(`{"title":"hi"}`, false)

	cmd := &AddCmd{Format: "json", Meta: []string{"env=prod"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	ev, _ := s.Get(context.Background(), 1)
	hasEnv := false
	for _, m := range ev.Meta {
		if m.Key == "env" && m.Value == "prod" {
			hasEnv = true
		}
	}
	if !hasEnv {
		t.Errorf("Meta = %v, want env=prod from --meta fallback", ev.Meta)
	}
}

func TestAddCmd_SplitsTitleBody(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{
		Args:   []string{"deployed v1.2 to staging. needed a manual restart"},
		Author: "alice",
	}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.Title != "deployed v1.2 to staging" || ev.Body != "needed a manual restart" {
		t.Errorf("got (title=%q, body=%q), want (deployed v1.2 to staging, needed a manual restart)",
			ev.Title, ev.Body)
	}
}

func TestAddCmd_NoSeparator(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"v1.2.3 released"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if events[0].Title != "v1.2.3 released" || events[0].Body != "" {
		t.Errorf("got (title=%q, body=%q), want (v1.2.3 released, '')", events[0].Title, events[0].Body)
	}
}

func TestAddCmd_TimePrefixSetsTimeAndStripsTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"9:30: had coffee"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, err := s.Get(context.Background(), 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if ev.Title != "had coffee" {
		t.Errorf("title = %q, want %q (time prefix stripped)", ev.Title, "had coffee")
	}
	got := ev.CreatedAt.Local()
	now := time.Now()
	if got.Hour() != 9 || got.Minute() != 30 {
		t.Errorf("CreatedAt time = %02d:%02d, want 09:30", got.Hour(), got.Minute())
	}
	if got.Year() != now.Year() || got.Month() != now.Month() || got.Day() != now.Day() {
		t.Errorf("CreatedAt date = %v, want today", got)
	}
}

func TestAddCmd_TimePrefixSkippedWithTimeFlag(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	// --time is the explicit override; the title keeps its literal prefix.
	cmd := &AddCmd{Args: []string{"9:30: had coffee"}, Author: "alice", Time: "2026-04-01 08:00"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Title != "9:30: had coffee" {
		t.Errorf("title = %q, want literal %q (--time skips prefix detection)", ev.Title, "9:30: had coffee")
	}
	got := ev.CreatedAt.Local()
	if got.Year() != 2026 || got.Month() != 4 || got.Day() != 1 || got.Hour() != 8 {
		t.Errorf("CreatedAt = %v, want 2026-04-01 08:00 from --time", got)
	}
}

func TestAddCmd_RelativeTimePrefix(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"yesterday at 9am: backfilled logs"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Title != "backfilled logs" {
		t.Errorf("title = %q, want %q", ev.Title, "backfilled logs")
	}
	got := ev.CreatedAt.Local()
	want := time.Now().AddDate(0, 0, -1)
	if got.Year() != want.Year() || got.Month() != want.Month() || got.Day() != want.Day() {
		t.Errorf("date = %v, want yesterday %v", got, want)
	}
	if got.Hour() != 9 || got.Minute() != 0 {
		t.Errorf("time = %02d:%02d, want 09:00", got.Hour(), got.Minute())
	}
}

func TestAddCmd_RelativeTimeFlag(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"retro note"}, Author: "alice", Time: "2 days ago"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), 1)
	got := ev.CreatedAt.Local()
	want := time.Now().AddDate(0, 0, -2)
	if got.Year() != want.Year() || got.Month() != want.Month() || got.Day() != want.Day() {
		t.Errorf("date = %v, want 2 days ago %v", got, want)
	}
}

func TestAddCmd_NonTimePrefixStoredVerbatim(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{"Meeting: discuss roadmap"}, Author: "alice"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	ev, _ := s.Get(context.Background(), 1)
	if ev.Title != "Meeting: discuss roadmap" {
		t.Errorf("title = %q, want verbatim (non-time prefix)", ev.Title)
	}
}

func TestAddCmd_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{". body only"}, Author: "alice"}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}
