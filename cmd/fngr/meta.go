package main

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/render"
)

type MetaCmd struct {
	List   MetaListCmd   `cmd:"" default:"withargs" help:"List metadata, optionally filtered (default)."`
	Rename MetaRenameCmd `cmd:"" help:"Rename a metadata entry across all events, merging into the target if it already exists."`
	Delete MetaDeleteCmd `cmd:"" help:"Delete a metadata entry across all events."`
}

type MetaListCmd struct {
	Search string `help:"Filter: bare key (e.g. 'tag'), key=value, @person, or #tag." short:"S"`
}

func (c *MetaListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	// No emptiness test here: parseMetaFilter answers an absent filter with
	// the zero Meta, which is the unfiltered listing. Two spellings of "no
	// filter" is one more than the number of ways to get it wrong.
	m, err := parseMetaFilter(c.Search)
	if err != nil {
		return err
	}
	opts := event.ListMetaOpts{Key: m.Key, Value: m.Value}

	counts, err := s.ListMeta(ctx, opts)
	if err != nil {
		return err
	}

	if len(counts) == 0 {
		reportNone(io.Err, "metadata")
		return nil
	}

	// One padded cell per row, `key=value` whole: the entry is one string and
	// only its right edge is a column. Padding the key instead gave the `=` a
	// column of its own whose width came from whichever rows the query
	// returned, so a filtered and an unfiltered listing spelled the same entry
	// two ways.
	//
	// Escape before measuring — meta values come from event bodies, so
	// `@josé\x1b[2J` is a name someone can type — and measure in runes,
	// because that is what `%-*s` pads to.
	pairs := make([]string, len(counts))
	width := 0
	for i, mc := range counts {
		pairs[i] = displayCell(mc.Key) + "=" + displayCell(mc.Value)
		width = max(width, utf8.RuneCountInString(pairs[i]))
	}
	for i, pair := range pairs {
		fmt.Fprintf(io.Out, "%-*s  (%d)\n", width, pair, counts[i].Count)
	}
	return nil
}

// maxMetaCell caps each half of a `fngr meta` entry — the key and the value
// separately, so a row is at most `2*maxMetaCell+1` wide. The column width is
// the longest cell in the listing and applies to every row, so a single
// oversized value used to pad all the others out to its length: 200 events with
// short meta plus one 1 MB value rendered 202 MB, ~200x what was stored. Meta
// is content-controlled — body tags, `--meta`, `--format=json` — so the bound
// has to be a constant rather than a hope. 60 is comfortably past any real tag,
// person, or key=value a journal carries; `fngr -S key=value --format=json`
// prints a clamped value in full.
const maxMetaCell = 60

// metaEllipsis marks a cell clampCell had to cut. One rune, one column.
const metaEllipsis = "…"

// displayCell prepares one cell of the `fngr meta` listing: control characters
// escaped, then clamped to maxMetaCell runes.
func displayCell(s string) string {
	// Cut the raw value first. render.SanitizeLine walks every byte it is
	// handed and copies the lot when anything needs escaping, so a megabyte
	// value cost ~2 ms and ~1 MB per row to print sixty characters — the output
	// amplification was only half of it. One rune past the cap is what makes
	// the shortcut free of consequence: escaping never shrinks a string, so a
	// prefix that long still sanitizes to more than the cap and so still gets
	// clamped, and escaping is per-rune, so the runes that survive the clamp
	// are the same either way.
	raw, _ := cutRunes(s, maxMetaCell+1)
	return clampCell(render.SanitizeLine(raw))
}

// clampCell truncates s to maxMetaCell columns, spending the last one on an
// ellipsis where it cut.
//
// Columns here means runes, matching what `%-*s` pads to. Not terminal cells:
// a double-width CJK value still pads short, which needs a width table nothing
// else in fngr wants and leaves a ragged column at worst — where the unclamped
// width was a 200x amplification.
func clampCell(s string) string {
	if _, more := cutRunes(s, maxMetaCell); !more {
		return s
	}
	cut, _ := cutRunes(s, maxMetaCell-1)
	return cut + metaEllipsis
}

// cutRunes returns the first n runes of s plus whether s had any more. It stops
// decoding one rune past n, so a megabyte value costs n+1 rune decodes rather
// than a walk of the whole thing.
func cutRunes(s string, n int) (string, bool) {
	count := 0
	for i := range s {
		if count == n {
			return s[:i], true
		}
		count++
	}
	return s, false
}

type MetaRenameCmd struct {
	Old   string `arg:"" help:"Old entry: key=value, @person, or #tag."`
	New   string `arg:"" help:"New entry: same forms as <old>."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaRenameCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	oldM, err := parse.MetaArg(c.Old)
	if err != nil {
		return err
	}
	newM, err := parse.MetaArg(c.New)
	if err != nil {
		return err
	}

	count, err := s.CountMeta(ctx, oldM.Key, oldM.Value)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no metadata matching %s=%s", oldM.Key, oldM.Value)
	}

	if !c.Force {
		// A rename onto an existing entry merges, and merging destroys rows:
		// an event carrying both ends up with one, so the totals go down.
		// Say so before asking — "Renamed N occurrences" on its own reads
		// as a pure move.
		merge := ""
		if newM != oldM {
			existing, err := s.CountMeta(ctx, newM.Key, newM.Value)
			if err != nil {
				return err
			}
			if existing > 0 {
				merge = fmt.Sprintf(" (%s=%s already on %d; events with both merge into one)",
					newM.Key, newM.Value, existing)
			}
		}

		prompt := fmt.Sprintf("Rename %s of %s=%s to %s=%s%s? [Y/n] ",
			plural(count, "occurrence"), oldM.Key, oldM.Value, newM.Key, newM.Value, merge)
		ok, err := confirm(io.In, io.Out, prompt, true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	affected, err := s.UpdateMeta(ctx, oldM.Key, oldM.Value, newM.Key, newM.Value)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Renamed %s\n", plural(affected, "occurrence"))
	return nil
}

type MetaDeleteCmd struct {
	Meta  string `arg:"" help:"Entry to delete: key=value, @person, or #tag."`
	Force bool   `help:"Skip confirmation prompt." short:"f"`
}

func (c *MetaDeleteCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	m, err := parse.MetaArg(c.Meta)
	if err != nil {
		return err
	}

	count, err := s.CountMeta(ctx, m.Key, m.Value)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("no metadata matching %s=%s", m.Key, m.Value)
	}

	if !c.Force {
		prompt := fmt.Sprintf("Delete %s of %s=%s? [y/N] ", plural(count, "occurrence"), m.Key, m.Value)
		ok, err := confirm(io.In, io.Out, prompt, false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(io.Out, "Aborted.")
			return nil
		}
	}

	n, err := s.DeleteMeta(ctx, m.Key, m.Value)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Deleted %s\n", plural(n, "occurrence"))
	return nil
}

// parseMetaFilter accepts the same shorthand as parse.MetaArg
// (@person, #tag, key=value) plus a bare key (e.g. "tag") that filters
// by key only. Returned Meta carries an empty Value for the bare-key
// case. Used only by MetaListCmd's -S filter.
func parseMetaFilter(s string) (parse.Meta, error) {
	if s == "" {
		return parse.Meta{}, nil
	}
	if s[0] != '@' && s[0] != '#' && !strings.Contains(s, "=") {
		// Unvalidated on purpose. This filter reaches ListMeta, a plain
		// `WHERE key = ?` — the -S *expression* tokenizer is nowhere on this
		// path, so nothing a key can contain makes it unmatchable here, and
		// a query path is what finds the rows a mint rule refuses to create.
		// It used to test MetaNameRe, so `fngr meta -S ticket.id` was refused
		// by the one command that lists metadata for a key `-m` stores
		// happily. Testing parse.ValidateMeta's key half instead would keep
		// the bug's shape at a different width.
		return parse.Meta{Key: s}, nil
	}
	return parse.MetaArg(s)
}
