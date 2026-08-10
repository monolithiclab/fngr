package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

// jsonAddInput is the wire shape for one event in --format=json input.
// Pointer types distinguish "field omitted" (apply CLI/built-in default)
// from "field present" (JSON value wins, even if zero/empty).
type jsonAddInput struct {
	// ID is the record's id in the database it came from. It is never
	// inserted — the target database assigns its own — but it is accepted
	// (rather than rejected by DisallowUnknownFields, as it used to be) so
	// that `fngr --format=json` output can be piped straight back in, and
	// so sibling records can refer to it through parent_id.
	ID        *int64      `json:"id"`
	Title     string      `json:"title"`
	Body      string      `json:"body"`
	ParentID  *int64      `json:"parent_id"`
	CreatedAt *string     `json:"created_at"`
	Meta      [][2]string `json:"meta"`
}

// cliDefaults bundles the parsed CLI flag values that may be applied
// when a JSON record omits the corresponding field. Computed once
// before the per-record loop.
type cliDefaults struct {
	parent *int64
	time   *time.Time
	meta   []parse.Meta // already parsed from --meta key=value flags
}

// maxJSONBatchSize caps the number of records accepted in a single
// `add --format=json` invocation. Each record becomes one INSERT inside
// a single transaction; bound this to keep tx times reasonable and to
// surface oversized input to the user instead of silently grinding.
const maxJSONBatchSize = 10000

// parseJSONAddInput decodes raw as either a JSON object (single record)
// or a JSON array (batch). The shape is dispatched on the first
// non-whitespace character so error messages stay scoped to the actual
// input shape. Unknown fields are rejected to surface typos
// (`{"txet": ...}`) instead of silently dropping data.
func parseJSONAddInput(raw string) ([]jsonAddInput, error) {
	trimmed := strings.TrimLeft(raw, " \t\r\n")
	if strings.HasPrefix(trimmed, "[") {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		var batch []jsonAddInput
		if err := dec.Decode(&batch); err != nil {
			return nil, fmt.Errorf("--format=json: %w", wireTypeError(err))
		}
		if len(batch) > maxJSONBatchSize {
			return nil, fmt.Errorf("--format=json: batch size %d exceeds limit %d (split into smaller batches)", len(batch), maxJSONBatchSize)
		}
		return batch, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var one jsonAddInput
	if err := dec.Decode(&one); err != nil {
		return nil, fmt.Errorf("--format=json: %w", wireTypeError(err))
	}
	return []jsonAddInput{one}, nil
}

// wireTypeError restates encoding/json's type mismatch in terms of the wire
// format, and passes every other decode error through untouched.
//
// json.UnmarshalTypeError renders as `cannot unmarshal object into Go struct
// field jsonAddInput.meta of type [][2]string`, which names a private Go type
// and a Go declaration at someone holding a JSON file. The three facts worth
// keeping are all fields on it: which field, what arrived, what was wanted.
// The byte offset comes along because a batch runs to 10 000 records and the
// field name alone does not say which one — it is the only locator the decoder
// keeps, records having no index at this stage.
//
// A top-level mismatch (`42`, or an array of strings) has no Field at all, and
// is the case that leaks worst: `cannot unmarshal number into Go value of type
// main.jsonAddInput` names the private type outright. So the field name is a
// prefix rather than a precondition — skipping the translation whenever it was
// missing left exactly the message this function exists to replace.
func wireTypeError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		return err
	}
	where := "input"
	if typeErr.Field != "" {
		where = fmt.Sprintf("field %q", typeErr.Field)
	}
	return fmt.Errorf("%s: got %s, want %s (at byte %d)",
		where, typeErr.Value, wireTypeName(typeErr.Type), typeErr.Offset)
}

// wireTypeName names a Go type the way JSON spells it. Kind-level only: the
// message says which JSON *shape* was wanted, and the exact one is the README's
// job — "array of 2-element array of string" describes `meta` no better than
// "array" does, and reads worse.
//
// No pointer case: encoding/json indirects before it reports, so a *int64 field
// arrives here as int64 and the arm would be dead code carrying a recursion.
func wireTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Slice, reflect.Array:
		return "array"
	default:
		return "object"
	}
}

func (c *AddCmd) runJSON(s eventStore, io ioStreams, raw string) error {
	inputs, err := parseJSONAddInput(raw)
	if err != nil {
		return err
	}

	defaults, err := buildCLIDefaults(c, io)
	if err != nil {
		return err
	}

	byIndex, err := indexBySourceID(inputs)
	if err != nil {
		return err
	}

	addInputs := make([]event.AddInput, 0, len(inputs))
	// A record naming a clock its zone skips gets the same warning `fngr add`
	// gives, but only the first one does: a batch is capped at 10 000 records
	// and one line each would bury whatever the import was meant to say.
	skipped := 0
	for i, in := range inputs {
		ai, clockExists, err := jsonInputToAddInput(in, defaults, c.Author, i, byIndex)
		if err != nil {
			return err
		}
		if !clockExists {
			if skipped == 0 {
				warnSkippedClock(io.Err, false, *in.CreatedAt, *ai.CreatedAt)
			}
			skipped++
		}
		addInputs = append(addInputs, ai)
	}
	if skipped > 1 {
		fmt.Fprintf(io.Err, "warning: plus %s in this batch with a clock that does not exist\n",
			plural(int64(skipped-1), "record"))
	}

	ids, err := s.AddMany(context.Background(), addInputs)
	if err != nil {
		return err
	}
	fmt.Fprintf(io.Out, "Imported %s\n", plural(int64(len(ids)), "event"))
	return nil
}

func buildCLIDefaults(c *AddCmd, io ioStreams) (cliDefaults, error) {
	d := cliDefaults{parent: c.Parent}
	if c.Time != "" {
		t, _, _, exists, err := timefmt.ParsePartial(c.Time)
		if err != nil {
			return cliDefaults{}, fmt.Errorf("invalid --time value: %w", err)
		}
		// Warned here rather than per record: --time is one value the user
		// typed once, however many records fall back to it.
		warnSkippedClock(io.Err, exists, c.Time, t)
		d.time = &t
	}
	if len(c.Meta) > 0 {
		parsed, err := parse.FlagMeta(c.Meta)
		if err != nil {
			return cliDefaults{}, err
		}
		d.meta = parsed
	}
	return d, nil
}

// indexBySourceID maps each record's source `id` to its position in the batch,
// so a `parent_id` naming another record in the same batch can be rewritten as
// a batch-relative reference. Without this the id was carried across as a
// literal integer into a database with its own autoincrement counter, which
// either failed the parent-exists check or — worse — landed on an unrelated
// event and imported a plausible but wrong tree.
func indexBySourceID(inputs []jsonAddInput) (map[int64]int, error) {
	var byIndex map[int64]int
	for i, in := range inputs {
		if in.ID == nil {
			continue
		}
		if prev, dup := byIndex[*in.ID]; dup {
			return nil, fmt.Errorf("--format=json: record %d: id %d already used by record %d",
				i, *in.ID, prev)
		}
		if byIndex == nil {
			byIndex = make(map[int64]int, len(inputs))
		}
		byIndex[*in.ID] = i
	}
	return byIndex, nil
}

// jsonInputToAddInput converts one wire record. clockExists reports whether the
// record's own created_at names a wall clock the local zone has; it is true
// whenever the record said nothing about the time, since the CLI default it then
// inherits is warned about once by buildCLIDefaults. So a false here always
// means in.CreatedAt is non-nil, which is what lets runJSON quote it.
func jsonInputToAddInput(
	in jsonAddInput, defaults cliDefaults, defaultAuthor string, index int, byIndex map[int64]int,
) (ai event.AddInput, clockExists bool, err error) {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return event.AddInput{}, false, fmt.Errorf("--format=json: record %d: title is required", index)
	}
	body := strings.TrimSpace(in.Body)

	// A parent_id pointing at another record in this batch is resolved by
	// position; anything else is an id in the target database and is checked
	// against it. The --parent CLI default is always a target-database id, so
	// it is only consulted when the record itself says nothing.
	var parent *int64
	var parentIndex *int
	switch {
	case in.ParentID == nil:
		parent = defaults.parent
	default:
		if i, ok := byIndex[*in.ParentID]; ok {
			parentIndex = &i
		} else {
			parent = in.ParentID
		}
	}

	var createdAt *time.Time
	clockExists = true
	if in.CreatedAt != nil {
		// timefmt.ParsePartial accepts RFC3339 — what `--format=json` emits —
		// plus every layout --time takes, so hand-written import files can use
		// the same stamps as the rest of the CLI.
		t, _, _, exists, err := timefmt.ParsePartial(*in.CreatedAt)
		if err != nil {
			return event.AddInput{}, false, fmt.Errorf("--format=json: record %d: created_at: %w", index, err)
		}
		createdAt, clockExists = &t, exists
	} else {
		createdAt = defaults.time
	}

	// Meta resolution: JSON wins if present (including explicit empty);
	// otherwise CLI flag defaults apply.
	var explicit []parse.Meta
	if in.Meta != nil {
		explicit = make([]parse.Meta, 0, len(in.Meta))
		for j, pair := range in.Meta {
			// Pre-flighted here, rather than left to the writer, because
			// this is the entry point that takes 10 000 tuples at a time and
			// the record index is what makes the bad one findable.
			if err := parse.ValidateMeta(pair[0], pair[1]); err != nil {
				return event.AddInput{}, false, fmt.Errorf("--format=json: record %d: meta[%d]: %w", index, j, err)
			}
			explicit = append(explicit, parse.Meta{Key: pair[0], Value: pair[1]})
		}
	} else {
		explicit = defaults.meta
	}

	// Same merge the text path gets: an explicit `author` replaces the CLI
	// default instead of joining it, and body-tag extraction runs against
	// title+body so tags from either are picked up.
	merged, err := event.MergeMeta(parse.EventText(title, body), explicit, defaultAuthor)
	if err != nil {
		return event.AddInput{}, false, fmt.Errorf("--format=json: record %d: %w", index, err)
	}

	if event.AuthorOf(merged) == "" {
		return event.AddInput{}, false, fmt.Errorf("--format=json: record %d: author is required (set meta.author, --author, FNGR_AUTHOR, or $USER)", index)
	}

	return event.AddInput{
		Title:       title,
		Body:        body,
		ParentID:    parent,
		ParentIndex: parentIndex,
		Meta:        merged,
		CreatedAt:   createdAt,
	}, clockExists, nil
}
