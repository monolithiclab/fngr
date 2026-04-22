package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	Text      string      `json:"text"`
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

// parseJSONAddInput tries to unmarshal raw as an array first; falls back
// to a single object if that fails. Returns the single-object error if
// both attempts fail (more informative for the common single-event case).
func parseJSONAddInput(raw string) ([]jsonAddInput, error) {
	data := []byte(raw)
	var batch []jsonAddInput
	if err := json.Unmarshal(data, &batch); err == nil {
		return batch, nil
	}
	var one jsonAddInput
	if err := json.Unmarshal(data, &one); err != nil {
		return nil, fmt.Errorf("--format=json: %w", err)
	}
	return []jsonAddInput{one}, nil
}

func (c *AddCmd) runJSON(s eventStore, io ioStreams, raw string) error {
	inputs, err := parseJSONAddInput(raw)
	if err != nil {
		return err
	}

	defaults, err := buildCLIDefaults(c)
	if err != nil {
		return err
	}

	addInputs := make([]event.AddInput, 0, len(inputs))
	for i, in := range inputs {
		ai, err := jsonInputToAddInput(in, defaults, c.Author, i)
		if err != nil {
			return err
		}
		addInputs = append(addInputs, ai)
	}

	ids, err := s.AddMany(context.Background(), addInputs)
	if err != nil {
		return err
	}
	if len(ids) == 1 {
		fmt.Fprintln(io.Out, "Imported 1 event")
	} else {
		fmt.Fprintf(io.Out, "Imported %d events\n", len(ids))
	}
	return nil
}

func buildCLIDefaults(c *AddCmd) (cliDefaults, error) {
	d := cliDefaults{parent: c.Parent}
	if c.Time != "" {
		t, err := timefmt.Parse(c.Time)
		if err != nil {
			return cliDefaults{}, fmt.Errorf("invalid --time value: %w", err)
		}
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

func jsonInputToAddInput(in jsonAddInput, defaults cliDefaults, defaultAuthor string, index int) (event.AddInput, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return event.AddInput{}, fmt.Errorf("--format=json: record %d: text is required", index)
	}

	parent := in.ParentID
	if parent == nil {
		parent = defaults.parent
	}

	var createdAt *time.Time
	if in.CreatedAt != nil {
		t, err := time.Parse(time.RFC3339, *in.CreatedAt)
		if err != nil {
			return event.AddInput{}, fmt.Errorf("--format=json: record %d: created_at: %w", index, err)
		}
		createdAt = &t
	} else {
		createdAt = defaults.time
	}

	// Meta resolution: JSON wins if present (including explicit empty);
	// otherwise CLI flag defaults apply.
	var explicit []parse.Meta
	if in.Meta != nil {
		explicit = make([]parse.Meta, 0, len(in.Meta))
		for j, pair := range in.Meta {
			if pair[0] == "" {
				return event.AddInput{}, fmt.Errorf("--format=json: record %d: meta[%d]: empty key", index, j)
			}
			explicit = append(explicit, parse.Meta{Key: pair[0], Value: pair[1]})
		}
	} else {
		explicit = defaults.meta
	}

	// Merge explicit meta + body tags + default author with dedup. CollectMeta
	// would inject defaultAuthor unconditionally, so we hand-roll the merge
	// here to honour an explicit JSON `author` entry instead.
	merged := mergeMetaForJSON(text, explicit, defaultAuthor)

	hasAuthor := false
	for _, m := range merged {
		if m.Key == event.MetaKeyAuthor {
			hasAuthor = true
			break
		}
	}
	if !hasAuthor {
		return event.AddInput{}, fmt.Errorf("--format=json: record %d: author is required (set meta.author, --author, FNGR_AUTHOR, or $USER)", index)
	}

	return event.AddInput{
		Text:      text,
		ParentID:  parent,
		Meta:      merged,
		CreatedAt: createdAt,
	}, nil
}

// mergeMetaForJSON builds the final meta list for a JSON record. It applies
// the same merge rules as event.CollectMeta — author first (unless the
// explicit list already names one), then body-derived tags, then explicit
// entries — with (key, value) dedup. Unlike CollectMeta the default author
// is suppressed when the explicit meta already includes an `author` entry,
// so JSON records can override author per-event.
func mergeMetaForJSON(text string, explicit []parse.Meta, defaultAuthor string) []parse.Meta {
	seen := make(map[parse.Meta]struct{})
	var result []parse.Meta
	add := func(m parse.Meta) {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			result = append(result, m)
		}
	}

	hasExplicitAuthor := false
	for _, m := range explicit {
		if m.Key == event.MetaKeyAuthor {
			hasExplicitAuthor = true
			break
		}
	}
	if !hasExplicitAuthor && defaultAuthor != "" {
		add(parse.Meta{Key: event.MetaKeyAuthor, Value: defaultAuthor})
	}

	for _, m := range parse.BodyTags(text) {
		add(m)
	}
	for _, m := range explicit {
		add(m)
	}
	return result
}
