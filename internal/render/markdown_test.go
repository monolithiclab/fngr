package render

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

// mdEvent builds an event.Event with a fixed timestamp, body, and optional
// meta tuples. ID is a fixed dummy since markdown output never references it.
func mdEvent(ts time.Time, text string, meta ...parse.Meta) event.Event {
	return event.Event{
		ID:        1,
		Text:      text,
		CreatedAt: ts,
		Meta:      meta,
	}
}

func TestMarkdown_Empty(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := Markdown(&b, nil); err != nil {
		t.Fatalf("Markdown(nil): %v", err)
	}
	if got := b.String(); got != "" {
		t.Errorf("Markdown(nil) = %q, want empty", got)
	}
	b.Reset()
	if err := Markdown(&b, []event.Event{}); err != nil {
		t.Fatalf("Markdown([]): %v", err)
	}
	if got := b.String(); got != "" {
		t.Errorf("Markdown([]) = %q, want empty", got)
	}
}

func TestMarkdown_SingleEvent_NoMeta(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "quick standup")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — quick standup\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_SingleEvent_WithMeta(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "quick standup",
		parse.Meta{Key: "location", Value: "cafe"},
		parse.Meta{Key: "author", Value: "nicolas"},
	)}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — quick standup\n  author=nicolas location=cafe\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_MultipleEventsSameDate(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "first"),
		mdEvent(time.Date(2026, 4, 22, 21, 32, 0, 0, time.Local), "second"),
	}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — first\n- 9.32pm — second\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_MultipleEventsDifferentDates(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "today"),
		mdEvent(time.Date(2026, 4, 21, 21, 32, 0, 0, time.Local), "yesterday"),
	}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — today\n\n## 2026-04-21\n\n- 9.32pm — yesterday\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_MultilineBody(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 21, 32, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "line one\nline two\nline three",
		parse.Meta{Key: "author", Value: "nicolas"},
	)}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 9.32pm — line one\n  line two\n  line three\n  author=nicolas\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_EmptyBody(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — \n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_EmptyBodyWithMeta(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "",
		parse.Meta{Key: "author", Value: "nicolas"},
	)}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	want := "## 2026-04-22\n\n- 8.15am — \n  author=nicolas\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_VerbatimSpecials(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "# heading [link](url) *bold* _underscore_")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if !strings.Contains(b.String(), "# heading [link](url) *bold* _underscore_") {
		t.Errorf("Markdown stripped or escaped specials:\n%s", b.String())
	}
}

func TestMarkdown_CRLFNormalization(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local)
	events := []event.Event{mdEvent(ts, "first\r\nsecond")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if strings.Contains(b.String(), "\r") {
		t.Errorf("Markdown leaked \\r:\n%q", b.String())
	}
	want := "## 2026-04-22\n\n- 8.15am — first\n  second\n"
	if got := b.String(); got != want {
		t.Errorf("Markdown:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestMarkdown_RespectsInputOrder(t *testing.T) {
	t.Parallel()
	earlier := mdEvent(time.Date(2026, 4, 21, 11, 4, 0, 0, time.Local), "earlier")
	later := mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "later")

	tests := []struct {
		name   string
		events []event.Event
		first  string
		second string
	}{
		{"desc", []event.Event{later, earlier}, "## 2026-04-22", "## 2026-04-21"},
		{"asc", []event.Event{earlier, later}, "## 2026-04-21", "## 2026-04-22"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := Markdown(&b, tt.events); err != nil {
				t.Fatalf("Markdown: %v", err)
			}
			i1 := strings.Index(b.String(), tt.first)
			i2 := strings.Index(b.String(), tt.second)
			if i1 < 0 || i2 < 0 || i1 >= i2 {
				t.Errorf("expected %q before %q\noutput:\n%s", tt.first, tt.second, b.String())
			}
		})
	}
}

func TestMarkdown_LocalTimezoneBucketing(t *testing.T) {
	// Mutates package-global time.Local — must NOT use t.Parallel.
	prev := time.Local
	t.Cleanup(func() { time.Local = prev })

	time.Local = time.FixedZone("PT", -7*3600)

	// 02:00 UTC on April 22 is 19:00 PT on April 21.
	ts := time.Date(2026, 4, 22, 2, 0, 0, 0, time.UTC)
	events := []event.Event{mdEvent(ts, "near midnight UTC")}

	var b bytes.Buffer
	if err := Markdown(&b, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if !strings.Contains(b.String(), "## 2026-04-21") {
		t.Errorf("expected ## 2026-04-21 (PT view), got:\n%s", b.String())
	}
	if !strings.Contains(b.String(), "7.00pm") {
		t.Errorf("expected 7.00pm time, got:\n%s", b.String())
	}
}

func TestMarkdownStream_Empty(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := MarkdownStream(&b, staticSeq(nil)); err != nil {
		t.Fatalf("MarkdownStream: %v", err)
	}
	if got := b.String(); got != "" {
		t.Errorf("empty stream produced %q, want empty", got)
	}
}

func TestMarkdownStream_MatchesMarkdown(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "first",
			parse.Meta{Key: "author", Value: "nicolas"},
		),
		mdEvent(time.Date(2026, 4, 22, 21, 32, 0, 0, time.Local), "second\nsecond line"),
		mdEvent(time.Date(2026, 4, 21, 11, 4, 0, 0, time.Local), "previous day",
			parse.Meta{Key: "tag", Value: "ship"},
		),
	}

	var slow, fast bytes.Buffer
	if err := Markdown(&slow, events); err != nil {
		t.Fatalf("Markdown: %v", err)
	}
	if err := MarkdownStream(&fast, staticSeq(events)); err != nil {
		t.Fatalf("MarkdownStream: %v", err)
	}
	if slow.String() != fast.String() {
		t.Errorf("MarkdownStream != Markdown\n--- Markdown ---\n%s\n--- Stream ---\n%s",
			slow.String(), fast.String())
	}
}

func TestMarkdownStream_PropagatesError(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		mdEvent(time.Date(2026, 4, 22, 8, 15, 0, 0, time.Local), "ok"),
	}
	wantErr := errors.New("boom")

	var b bytes.Buffer
	err := MarkdownStream(&b, errorAtSeq(events, 1, wantErr))
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if !strings.Contains(b.String(), "ok") {
		t.Errorf("partial output not flushed:\n%s", b.String())
	}
}
