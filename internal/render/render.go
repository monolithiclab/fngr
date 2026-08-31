package render

import (
	"bytes"
	"cmp"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// Output format identifiers used by the CLI's --format flag and the
// render dispatchers below. cmd/fngr wires these into Kong via kongVars
// so the flag enum and default stay in lockstep with this package.
const (
	FormatTree     = "tree"
	FormatFlat     = "flat"
	FormatJSON     = "json"
	FormatCSV      = "csv"
	FormatText     = "text"
	FormatMarkdown = "md"
)

// formatAliases maps every accepted spelling that is not itself canonical.
// `md` stays the canonical name — it is what the flag defaults and the docs
// use — but `markdown` is the word people reach for, and being told it is not
// a format by a tool that has one is a papercut with no upside.
var formatAliases = map[string]string{
	"markdown": FormatMarkdown,
}

// Canonical resolves an accepted spelling to the name the dispatchers switch
// on. Anything unrecognised is returned unchanged: Kong's enum has already
// refused what the CLI does not accept, and the dispatchers fall back to their
// own default for a value that reaches them another way.
func Canonical(format string) string {
	if c, ok := formatAliases[format]; ok {
		return c
	}
	return format
}

// ListFormats are the formats accepted by Events and EventsStream, aliases
// included — the slice is the Kong enum, so a spelling missing here is
// rejected at parse time however well Canonical would resolve it.
var ListFormats = withAliases(FormatTree, FormatFlat, FormatJSON, FormatCSV, FormatMarkdown)

// EventFormats are the formats accepted by SingleEvent, on the same terms.
// The vocabularies are deliberately different — `tree`/`flat` need a set of
// events, `text` is the one-event view — which is exactly why neither may
// simply take every alias.
var EventFormats = withAliases(FormatText, FormatJSON, FormatCSV, FormatMarkdown)

// AddFormats are the *input* formats `fngr add --format` accepts. The alias
// table is about output spellings and none of its entries resolve into this
// vocabulary today, but it goes through withAliases anyway so that adding one
// that does needs no second thought.
var AddFormats = withAliases(FormatText, FormatJSON)

// withAliases expands a vocabulary with the aliases of the formats in it, and
// only those. Listing them by hand instead invites the alias into a vocabulary
// that cannot render it: `txt` → `text` in ListFormats would parse, resolve,
// miss every case in EventsStream's switch and fall through to flat — the user
// asks for one format, gets another, exit 0. Deriving makes that unspellable.
//
// Aliases are appended in sorted order because the slice is joined into Kong's
// enum, and the enum is in the error text a rejected --format prints.
func withAliases(formats ...string) []string {
	for _, alias := range slices.Sorted(maps.Keys(formatAliases)) {
		if slices.Contains(formats, formatAliases[alias]) {
			formats = append(formats, alias)
		}
	}
	return formats
}

// nowFunc is the relative-stamp anchor. Production never reassigns it;
// tests swap it via pinNow inside non-parallel subtests.
var nowFunc = time.Now

func formatLocalStamp(t time.Time) string {
	return timefmt.FormatRelativePadded(t, nowFunc())
}

func formatLocalDateTime(t time.Time) string {
	return t.Local().Format(timefmt.DateTimeFormat)
}

func eventAuthor(ev event.Event) string { return event.AuthorOf(ev.Meta) }

// formatEventLine renders the one-line form shared by tree, flat and their
// streaming variants. It carries no event id: the id is how other verbs
// (`event show`, `delete`, ...) address a row, not something the list view
// needs to say about itself. author and text are sanitized here rather than
// at each call site, so no caller of this helper can forget. Formats that
// lay out their own lines sanitize for themselves: Markdown here, and the
// `meta` listing over in cmd/fngr.
func formatEventLine(date, author, text string) string {
	return fmt.Sprintf("%s  %s  %s", date, SanitizeLine(author), SanitizeLine(text))
}

// Events writes a list of events in the requested format. Supported formats
// are FormatTree (default), FormatFlat, FormatJSON, FormatCSV, FormatMarkdown.
func Events(w io.Writer, format string, events []event.Event) error {
	switch Canonical(format) {
	case FormatCSV:
		return CSV(w, events)
	case FormatFlat:
		return Flat(w, events)
	case FormatJSON:
		return JSON(w, events)
	case FormatMarkdown:
		return Markdown(w, events)
	default:
		return Tree(w, events)
	}
}

// SingleEvent writes one event in the requested format. Supported formats
// are FormatText (default), FormatJSON, FormatCSV, FormatMarkdown.
func SingleEvent(w io.Writer, format string, ev *event.Event) error {
	switch Canonical(format) {
	case FormatCSV:
		return CSV(w, []event.Event{*ev})
	case FormatJSON:
		return JSONEvent(w, ev)
	case FormatMarkdown:
		return Markdown(w, []event.Event{*ev})
	default:
		return Event(w, ev)
	}
}

// Tree branch drawing. connector prefixes a node's own line; continuation is
// what sits under it while later siblings are still to come. The ordinary
// pair is three display columns wide and the orphan pair is four; what
// matters is that each pair agrees with itself, so a subtree stays aligned
// under either kind of root.
const (
	treeConnector    = "\u251c\u2500 "
	treeContinuation = "\u2502  "
	treeCorner       = "\u2514\u2500 "
	treeBlank        = "   "

	// orphanConnector marks an event whose parent exists but fell outside
	// the result set — under `--limit`, or a filter that matched the child
	// and not the parent. Rendering it flush left would state, falsely,
	// that it has no parent. The ellipsis stands for the elided ancestry.
	orphanConnector = "\u22ef\u2514\u2500 "
	orphanBlank     = "    "
)

// Tree writes events as an indented parent/child tree. Events whose
// parent_id is not present in the input slice still render at top level, so
// a `--limit`-truncated query produces well-formed output, but they carry
// the orphanConnector marker rather than passing as true roots.
//
// Every event in the input appears in the output exactly once, whatever the
// topology says. A cyclic parent chain (which fngr cannot write but can be
// handed) leaves its members with no root above them, and the old code then
// found no roots, wrote nothing and exited 0 — a total data blackout reported
// as success. The sweep below draws whatever the root walk missed.
func Tree(w io.Writer, events []event.Event) error {
	if len(events) == 0 {
		return nil
	}

	byID := make(map[int64]int, len(events))
	children := make(map[int64][]int64)
	var roots []int64

	for i, ev := range events {
		byID[ev.ID] = i
	}
	for _, ev := range events {
		if ev.ParentID == nil {
			roots = append(roots, ev.ID)
			continue
		}
		if _, parentInSet := byID[*ev.ParentID]; parentInSet {
			children[*ev.ParentID] = append(children[*ev.ParentID], ev.ID)
		} else {
			roots = append(roots, ev.ID)
		}
	}

	t := &treeWriter{
		w:        w,
		events:   events,
		byID:     byID,
		children: children,
		visited:  make([]bool, len(events)),
	}
	for _, id := range roots {
		if err := t.top(id); err != nil {
			return err
		}
	}

	// Anything the root walk could not reach belongs to a cycle. node skips
	// whatever is already drawn, so the sweep needs no test of its own.
	for i := range events {
		if err := t.top(events[i].ID); err != nil {
			return err
		}
	}
	return nil
}

// treeWriter carries the recursion state for Tree. prefix is the indent
// standing to the left of the current node's children, held as one reusable
// buffer that is appended to on the way down and truncated on the way back
// up.
//
// It used to be two freshly concatenated strings per node, which every
// ancestor frame then held live on the stack: Σd = O(depth²) bytes that GC
// could not reclaim. A 50k-deep chain took 67.85 s and 7.0 GB. The output
// is inherently O(depth²) characters — a tree prints depth indent columns
// per line — but nothing has to be *allocated* to produce it.
type treeWriter struct {
	w        io.Writer
	events   []event.Event
	byID     map[int64]int
	children map[int64][]int64
	visited  []bool // indexed like events, via byID
	prefix   []byte
	line     []byte // scratch, so each node costs the writer one Write
}

// top writes id at the outer level, marking it as an orphan when it names a
// parent that is not drawn above it. That covers a child whose parent fell
// outside the result set (--limit, or a filter that matched only the child)
// and a cycle member the root walk could not reach, which make the same claim:
// this has a parent you cannot see from here.
func (t *treeWriter) top(id int64) error {
	if t.events[t.byID[id]].ParentID != nil {
		return t.node(id, orphanConnector, orphanBlank)
	}
	return t.node(id, "", "")
}

// node writes one line for id — the running prefix, this node's own
// connector, then the event — and recurses into its children under an
// extended prefix. Roots come in through the same door: an ordinary root
// just passes an empty connector.
//
// A node already drawn is skipped rather than drawn again. In a well-formed
// tree that cannot happen — one parent each, so one visit each — and it is
// what stops a cyclic chain from recursing until the stack gives out.
func (t *treeWriter) node(id int64, connector, continuation string) error {
	i := t.byID[id]
	if t.visited[i] {
		return nil
	}
	t.visited[i] = true
	ev := t.events[i]

	t.line = append(t.line[:0], t.prefix...)
	t.line = append(t.line, connector...)
	t.line = append(t.line,
		formatEventLine(formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Title)...)
	t.line = append(t.line, '\n')
	if _, err := t.w.Write(t.line); err != nil {
		return err
	}

	kids := t.children[id]
	base := len(t.prefix)
	t.prefix = append(t.prefix, continuation...)

	for i, kidID := range kids {
		kidConnector, kidContinuation := treeConnector, treeContinuation
		if i == len(kids)-1 {
			kidConnector, kidContinuation = treeCorner, treeBlank
		}
		if err := t.node(kidID, kidConnector, kidContinuation); err != nil {
			t.prefix = t.prefix[:base]
			return err
		}
	}

	t.prefix = t.prefix[:base]
	return nil
}

// slicedSeq adapts a materialized slice to the streaming signature, so every
// format but tree is written in exactly one place. Which of the two entry
// points a command reaches for depends on whether it could stream, not on what
// the user asked for, so `fngr --format=json` and `fngr event 1 -t
// --format=json` over the same rows have to diff clean. Delegating makes that
// structural instead of a pair of layout rules kept in step by hand — which
// they were not: the streamed JSON used to put each comma on a line of its own
// and a blank line before the `]`, and the buffered CSV discarded the write
// errors its streaming twin checked.
//
// Tree is the exception, and not by oversight: it needs the whole topology
// before it can draw a line, so it has no streaming form to delegate to.
//
// The error half is always nil — there is no read left to fail.
func slicedSeq(events []event.Event) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// Flat writes one line per event in input order: `date  author  text`.
// Parent/child topology is ignored; for that, use Tree.
func Flat(w io.Writer, events []event.Event) error {
	return FlatStream(w, slicedSeq(events))
}

type jsonEvent struct {
	ID        int64       `json:"id"`
	ParentID  *int64      `json:"parent_id,omitempty"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	CreatedAt string      `json:"created_at"`
	Meta      [][2]string `json:"meta,omitempty"`
}

func toJSONEvent(ev event.Event) jsonEvent {
	out := jsonEvent{
		ID:        ev.ID,
		ParentID:  ev.ParentID,
		Title:     ev.Title,
		Body:      ev.Body,
		CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
	}
	if len(ev.Meta) == 0 {
		return out
	}
	pairs := make([][2]string, len(ev.Meta))
	for i, m := range ev.Meta {
		pairs[i] = [2]string{m.Key, m.Value}
	}
	slices.SortFunc(pairs, func(a, b [2]string) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return cmp.Compare(a[1], b[1])
	})
	out.Meta = pairs
	return out
}

// JSON writes events as a single indented JSON array, suitable for
// round-tripping back through `fngr add --format=json`. Meta is emitted
// as `[[key, value], ...]` sorted by (key, value).
func JSON(w io.Writer, events []event.Event) error {
	return JSONStream(w, slicedSeq(events))
}

// JSONEvent writes one event as a single JSON object rather than a
// one-element array. `fngr event 5 --format=json` describes one event, so
// `jq '.title'` should answer with its title — against an array it returned
// null, and every reader had to know to write `.[0]`. The import side reads
// both shapes (`parseJSONAddInput` dispatches on the leading `[`), so the
// round trip is unaffected.
//
// Indented at the top level, unlike the elements JSONStream writes: a
// standalone object starts in column zero.
func JSONEvent(w io.Writer, ev *event.Event) error {
	data, err := json.MarshalIndent(toJSONEvent(*ev), "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

// CSV writes events as a CSV table with the columns
// `id, parent_id, created_at, author, title, body`. Meta tuples beyond
// `author` are not represented; for full meta, use JSON or Markdown.
func CSV(w io.Writer, events []event.Event) error {
	return CSVStream(w, slicedSeq(events))
}

// Event writes a single event in the human-readable detail layout used
// by `fngr event N` (ID / Parent / Date / Title / [body block] / Meta).
// When body is non-empty, a blank line and the raw body block follow
// the Title line. When body is empty, Meta follows directly after Title.
func Event(w io.Writer, ev *event.Event) error {
	if _, err := fmt.Fprintf(w, "ID:     %d\n", ev.ID); err != nil {
		return err
	}
	if ev.ParentID != nil {
		if _, err := fmt.Fprintf(w, "Parent: %d\n", *ev.ParentID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "Date:   %s\n", formatLocalDateTime(ev.CreatedAt)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Title:  %s\n", SanitizeLine(ev.Title)); err != nil {
		return err
	}
	if ev.Body != "" {
		// The body block is the one place newlines are the point, so it
		// keeps them; everything else that could drive the terminal still
		// goes. This path has no pager in front of it.
		if _, err := fmt.Fprintf(w, "\n%s\n", sanitizeBlock(ev.Body)); err != nil {
			return err
		}
	}

	if len(ev.Meta) > 0 {
		if _, err := fmt.Fprintln(w, "Meta:"); err != nil {
			return err
		}
		for _, m := range ev.Meta {
			if _, err := fmt.Fprintf(w, "  %s=%s\n", SanitizeLine(m.Key), SanitizeLine(m.Value)); err != nil {
				return err
			}
		}
	}

	return nil
}

// FlatStream is the streaming counterpart to Flat. It writes one line per
// event as the iterator yields. The first error from seq aborts and is
// returned.
func FlatStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	for ev, err := range seq {
		if err != nil {
			return err
		}
		line := formatEventLine(formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Title)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// CSVStream is the streaming counterpart to CSV. It writes the header
// followed by one row per event from seq.
func CSVStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "parent_id", "created_at", "author", "title", "body"}); err != nil {
		return err
	}
	for ev, err := range seq {
		if err != nil {
			cw.Flush()
			return err
		}
		parentID := ""
		if ev.ParentID != nil {
			parentID = strconv.FormatInt(*ev.ParentID, 10)
		}
		if err := cw.Write([]string{
			strconv.FormatInt(ev.ID, 10),
			parentID,
			ev.CreatedAt.UTC().Format(time.RFC3339),
			eventAuthor(ev),
			ev.Title,
			ev.Body,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// JSONStream is the streaming counterpart to JSON. It writes a JSON array
// where each element is encoded individually, so the full serialized blob
// is never held in memory. On error mid-stream the array is still closed
// with "]\n" so any captured output is syntactically valid JSON.
//
// The output must match `json.MarshalIndent(events, "", "  ")` byte for byte —
// that is the shape `fngr --format=json` has always emitted and the one the
// import side reads back — which is why the separators are written here rather
// than left to the encoder: json.Encoder terminates every value with a newline
// of its own, putting the comma on a line by itself and a blank line before
// the `]`. TestJSONStream_MatchesMarshalIndent is what pins that.
//
// The buffer is what makes the encoder usable at all here, not an optimization
// on top of it: Encode writes straight through, so its trailing newline can
// only be trimmed off a buffer. Marshalling each event afresh instead would be
// the obvious way to get the same bytes, and costs ~260 MB of garbage over a
// 250k listing against ~54 MB. Both are reused across the whole stream.
func JSONStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	var buf bytes.Buffer
	// Indent one level in: an element of `MarshalIndent(slice, "", "  ")`
	// carries its fields at four spaces and its closing brace at two.
	enc := json.NewEncoder(&buf)
	enc.SetIndent("  ", "  ")

	// Encoded by pointer out of a hoisted variable: jsonEvent is 88 bytes, so
	// passing it by value boxes a fresh copy onto the heap for every event,
	// where a pointer fits the interface word. Same bytes out, a third fewer
	// allocations over a large listing.
	var jev jsonEvent

	first := true
	var streamErr error
	for ev, err := range seq {
		if err != nil {
			streamErr = err
			break
		}
		buf.Reset()
		jev = toJSONEvent(ev)
		if err := enc.Encode(&jev); err != nil {
			return err
		}
		// Encode's own trailing newline is the one artefact left to undo.
		data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))

		lead := ",\n  "
		if first {
			lead = "[\n  "
		}
		if _, err := io.WriteString(w, lead); err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
		first = false
	}
	tail := "\n]\n"
	if first {
		tail = "[]\n"
	}
	if _, err := io.WriteString(w, tail); err != nil {
		return err
	}
	return streamErr
}

// EventsStream dispatches a streaming render. Tree is rejected because it
// requires the full slice for parent-child topology; callers that want
// tree must use Events with a materialized []Event.
func EventsStream(w io.Writer, format string, seq iter.Seq2[event.Event, error]) error {
	switch Canonical(format) {
	case FormatCSV:
		return CSVStream(w, seq)
	case FormatJSON:
		return JSONStream(w, seq)
	case FormatFlat:
		return FlatStream(w, seq)
	case FormatMarkdown:
		return MarkdownStream(w, seq)
	case FormatTree:
		return fmt.Errorf("EventsStream: tree format requires the full slice; use Events instead")
	default:
		return FlatStream(w, seq)
	}
}
