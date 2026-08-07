package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

func TestSanitizeLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "coffee with josé", "coffee with josé"},
		{"empty", "", ""},
		{"tab survives", "a\tb", "a\tb"},
		{"newline escaped", "a\nb", `a\nb`},
		{"carriage return escaped", "a\rb", `a\rb`},
		{"crlf escaped", "a\r\nb", `a\r\nb`},
		{"escape byte", "a\x1bb", `a\x1bb`},
		{"nul byte", "a\x00b", `a\x00b`},
		{"del", "a\x7fb", `a\x7fb`},
		{"c1 csi introducer", "a\u009bb", `a\x9bb`},
		{"c1 lower bound", "a\u0080b", `a\x80b`},
		{"c1 upper bound", "a\u009fb", `a\x9fb`},
		{"just past c1 is printable", "a\u00a0b", "a\u00a0b"},
		// The raw byte, not its UTF-8 encoding: this is the 8-bit CSI a
		// terminal actually acts on, and rune iteration hands it over as
		// utf8.RuneError.
		{"raw csi byte", "a\x9bb", `a\x9bb`},
		{"lone continuation byte", "a\x80b", `a\x80b`},
		// Invalid UTF-8 is escaped, never replaced: U+FFFD would discard what
		// the byte was, and the escape is the same whether or not something
		// else in the string forced the slow path.
		{"latin-1 tail", "caf\xe9", `caf\xe9`},
		{"latin-1 tail beside an escape", "caf\xe9\x1bX", `caf\xe9\x1bX`},
		{"bell", "a\ab", `a\x07b`},
		{
			"forged row",
			"benign\n99  4.00pm  root  SYSTEM: all clear",
			`benign\n99  4.00pm  root  SYSTEM: all clear`,
		},
		{
			"osc 8 hyperlink",
			"\x1b]8;;http://evil.example\x1b\\CLICK\x1b]8;;\x1b\\",
			`\x1b]8;;http://evil.example\x1b\CLICK\x1b]8;;\x1b\`,
		},
		{"erase display", "x\x1b[2J", `x\x1b[2J`},
		{"multibyte preserved", "田中さん #déploiement", "田中さん #déploiement"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeLine(tt.in); got != tt.want {
				t.Errorf("SanitizeLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"newline survives", "line one\nline two", "line one\nline two"},
		{"tab survives", "\tindented", "\tindented"},
		{"escape still escaped", "a\x1b[31mred", `a\x1b[31mred`},
		{"carriage return still escaped", "a\r\nb", `a\r` + "\nb"},
		{"nul still escaped", "a\x00b", `a\x00b`},
		{"plain untouched", "just a body", "just a body"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := sanitizeBlock(tt.in); got != tt.want {
				t.Errorf("sanitizeBlock(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitize_CleanTextIsNotRebuilt pins the fast path: nearly every string
// has nothing to escape, and those are returned as is rather than copied
// rune by rune into a builder.
//
// Not parallel: testing.AllocsPerRun panics in a parallel test.
func TestSanitize_CleanTextIsNotRebuilt(t *testing.T) {
	const clean = "a perfectly ordinary title with #tags and @people"
	if allocs := testing.AllocsPerRun(100, func() { _ = SanitizeLine(clean) }); allocs != 0 {
		t.Errorf("SanitizeLine on clean text allocated %v times, want 0", allocs)
	}
}

// controlEvent builds an event whose title, body and meta all carry control
// bytes, for the per-format assertions below.
func controlEvent() event.Event {
	return event.Event{
		ID:        1,
		Title:     "title\nforged\x1b[2J",
		Body:      "body line one\nline two\x1b]52;c;aGFja2Vk\a",
		CreatedAt: time.Date(2026, 4, 10, 9, 0, 0, 0, time.Local),
		Meta: []parse.Meta{
			{Key: event.MetaKeyAuthor, Value: "nico\x1b[31m"},
			{Key: "tag", Value: "ops\nfake"},
		},
	}
}

// TestFormats_EscapeControlBytes walks every human-facing renderer with the
// same hostile event. A raw ESC or a smuggled newline reaching the writer is
// the defect: the newline forges a row that reads as a real event, and the
// ESC drives the terminal directly — `fngr event N` and `fngr delete` have
// no pager in front of them.
func TestFormats_EscapeControlBytes(t *testing.T) {
	ev := controlEvent()
	pinNow(t, time.Date(2026, 4, 10, 12, 0, 0, 0, time.Local))

	tests := []struct {
		name   string
		render func(*bytes.Buffer) error
		// wantLines is the exact line count the format should produce, so a
		// smuggled newline shows up as an extra row.
		wantLines int
	}{
		{"flat", func(b *bytes.Buffer) error { return Flat(b, []event.Event{ev}) }, 1},
		{"tree", func(b *bytes.Buffer) error { return Tree(b, []event.Event{ev}) }, 1},
		{"event detail", func(b *bytes.Buffer) error { return Event(b, &ev) }, 9},
		{"markdown", func(b *bytes.Buffer) error { return Markdown(b, []event.Event{ev}) }, 7},
		{
			"flat stream",
			func(b *bytes.Buffer) error { return FlatStream(b, slicedSeq([]event.Event{ev})) },
			1,
		},
		{
			"markdown stream",
			func(b *bytes.Buffer) error { return MarkdownStream(b, slicedSeq([]event.Event{ev})) },
			7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			if err := tt.render(&b); err != nil {
				t.Fatalf("render: %v", err)
			}
			out := b.String()

			for _, r := range []rune{0x1b, 0x07} {
				if strings.ContainsRune(out, r) {
					t.Errorf("output carries a raw %#x:\n%q", r, out)
				}
			}
			if got := strings.Count(strings.TrimSuffix(out, "\n"), "\n") + 1; got != tt.wantLines {
				t.Errorf("output has %d lines, want %d — a newline was smuggled through:\n%q",
					got, tt.wantLines, out)
			}
		})
	}
}

// TestEvent_BodyKeepsRealNewlines is the counterweight: the detail view lays
// a body out over several lines on purpose, so sanitizing must not flatten
// it into one.
func TestEvent_BodyKeepsRealNewlines(t *testing.T) {
	t.Parallel()
	ev := event.Event{
		ID:        1,
		Title:     "title",
		Body:      "first\nsecond\n\tindented",
		CreatedAt: time.Date(2026, 4, 10, 9, 0, 0, 0, time.Local),
	}

	var b bytes.Buffer
	if err := Event(&b, &ev); err != nil {
		t.Fatalf("Event: %v", err)
	}
	if !strings.Contains(b.String(), "first\nsecond\n\tindented\n") {
		t.Errorf("body was not written verbatim:\n%q", b.String())
	}
}

// TestJSONCSV_KeepFullFidelity pins the deliberate exclusion of the two
// machine-readable formats, which is not the same trade in both. JSON escapes
// control bytes itself and losslessly, so sanitizing would only break the
// `fngr --format=json | fngr add --format=json` round trip. CSV does not
// escape them at all — csv.Writer quotes for structural safety and nothing
// more — and that is accepted rather than fixed: escaping would leave a
// reader unable to tell a stored `\x1b` from an escaped one. Anyone piping
// CSV straight to a terminal is reading raw data by choice.
func TestJSONCSV_KeepFullFidelity(t *testing.T) {
	t.Parallel()
	ev := controlEvent()

	var jb bytes.Buffer
	if err := JSON(&jb, []event.Event{ev}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if strings.ContainsRune(jb.String(), 0x1b) {
		t.Error("JSON emitted a raw ESC; encoding/json should have escaped it")
	}
	if !strings.Contains(jb.String(), `\u001b`) {
		t.Errorf("JSON did not preserve the ESC as an escape:\n%s", jb.String())
	}

	var cb bytes.Buffer
	if err := CSV(&cb, []event.Event{ev}); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	// CSV quotes an embedded newline rather than escaping it, which keeps
	// the record parseable; that is the encoder's contract, not ours.
	if !strings.Contains(cb.String(), "\"title\nforged") {
		t.Errorf("CSV did not quote the embedded newline:\n%q", cb.String())
	}
	// And the ESC goes out raw. Asserted so the trade stays a decision: if
	// this ever needs to change, it changes here first.
	if !strings.ContainsRune(cb.String(), 0x1b) {
		t.Errorf("CSV no longer passes control bytes through verbatim:\n%q", cb.String())
	}
}
