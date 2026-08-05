package event

import (
	"fmt"

	"github.com/monolithiclab/fngr/internal/parse"
)

// Well-known meta keys. The matching value's domain meaning is encoded
// in the key, so renderers and CLI verbs can recognise them by name.
const (
	// MetaKeyAuthor identifies the user who recorded the event. Auto-set
	// from --author / $FNGR_AUTHOR / $USER; single-valued and immutable
	// once the event exists (see protectedMetaKeys).
	MetaKeyAuthor = "author"
	// MetaKeyPeople holds names extracted from `@person` body shorthand.
	MetaKeyPeople = "people"
	// MetaKeyTag holds names extracted from `#tag` body shorthand.
	MetaKeyTag = "tag"
)

// event_meta.source values (migration 5). Provenance exists for exactly one
// decision: an edit that drops an inline `@bob` retracts the tag it minted,
// and must leave an identical tuple the operator asked for standing. Without
// it the two are the same row and the sync took both.
const (
	// metaSourceBody marks a tuple derived from `@person` / `#tag` in the
	// event's own title or body. Only these are removed by Update's sync.
	metaSourceBody = "body"
	// metaSourceExplicit marks a tuple the operator supplied — `--meta`,
	// `event tag`, `meta rename`. Also the column default, so any row an
	// older build wrote reads as explicit until migration 5 classifies it.
	metaSourceExplicit = "explicit"
)

// MergeMeta merges the meta sources for a new event into one deduped slice
// in a deterministic order: the author tuple, then body-derived `@person` /
// `#tag` tags, then the remaining explicit entries.
//
// An explicit `author` replaces defaultAuthor rather than joining it —
// author is single-valued, and appending both left two `author` rows on the
// event with the alphabetically-first one silently winning every render.
// For the same reason two explicit authors are an error, not a merge.
// defaultAuthor may be empty, in which case no author tuple is synthesised
// and the caller is responsible for deciding whether that is acceptable.
func MergeMeta(text string, explicit []parse.Meta, defaultAuthor string) ([]parse.Meta, error) {
	author := defaultAuthor
	explicitAuthor := false
	for _, m := range explicit {
		if m.Key != MetaKeyAuthor {
			continue
		}
		// An empty explicit author is rejected rather than ignored. Falling
		// back to defaultAuthor would silently do something other than what
		// was asked; keeping it would store a blank author that no verb can
		// repair, since author is protected against every meta mutation.
		if m.Value == "" {
			return nil, fmt.Errorf("%s cannot be empty", MetaKeyAuthor)
		}
		// Tracked with its own flag rather than by comparing against
		// defaultAuthor: `-m author=x -m author=y` must be rejected even
		// when x happens to equal the default.
		if explicitAuthor && m.Value != author {
			return nil, fmt.Errorf("conflicting %s values %q and %q: an event has exactly one author", MetaKeyAuthor, author, m.Value)
		}
		author, explicitAuthor = m.Value, true
	}

	bodyTags := parse.BodyTags(text)
	seen := make(map[parse.Meta]struct{})
	result := make([]parse.Meta, 0, 1+len(bodyTags)+len(explicit))
	add := func(m parse.Meta) {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			result = append(result, m)
		}
	}

	// Author first even when it came from `explicit`, so the tuple order is
	// the same however the author was supplied.
	if author != "" {
		add(parse.Meta{Key: MetaKeyAuthor, Value: author})
	}
	for _, m := range bodyTags {
		add(m)
	}
	for _, m := range explicit {
		add(m)
	}
	return result, nil
}

// AuthorOf returns the author recorded in meta, or "" if there is none. It is
// the single reading of a rule the write paths guarantee — at most one author
// tuple per event — so `render` and the CLI cannot drift from each other on
// what happens when that guarantee is somehow broken.
func AuthorOf(meta []parse.Meta) string {
	for _, m := range meta {
		if m.Key == MetaKeyAuthor {
			return m.Value
		}
	}
	return ""
}

// requireOneAuthor rejects a merged meta slice carrying two different authors
// or a blank one. MergeMeta already guarantees both for the two CLI paths;
// this is the same invariant restated at the writer, so a directly-built
// AddInput cannot create the row that AuthorOf reads arbitrarily and no meta
// verb is allowed to repair. Zero authors stays legal — the caller decides
// whether an unattributed event is acceptable.
func requireOneAuthor(meta []parse.Meta) error {
	author, found := "", false
	for _, m := range meta {
		if m.Key != MetaKeyAuthor {
			continue
		}
		if m.Value == "" {
			return fmt.Errorf("%s cannot be empty", MetaKeyAuthor)
		}
		if found && m.Value != author {
			return fmt.Errorf("conflicting %s values %q and %q: an event has exactly one author", MetaKeyAuthor, author, m.Value)
		}
		author, found = m.Value, true
	}
	return nil
}

// CollectMeta is MergeMeta over `--meta key=value` flag strings. Errors out
// if any flag fails to parse as `key=value`.
func CollectMeta(text string, flags []string, author string) ([]parse.Meta, error) {
	explicit, err := parse.FlagMeta(flags)
	if err != nil {
		return nil, err
	}
	return MergeMeta(text, explicit, author)
}

// metaSet indexes tuples for membership tests. parse.Meta is comparable, so
// the tuple is its own key.
func metaSet(tuples []parse.Meta) map[parse.Meta]struct{} {
	set := make(map[parse.Meta]struct{}, len(tuples))
	for _, m := range tuples {
		set[m] = struct{}{}
	}
	return set
}

// subtractMeta returns the tuples in a that are absent from b, preserving
// a's order. Used by Update to narrow the body-tag delete set to tags the
// edit actually removed.
func subtractMeta(a, b []parse.Meta) []parse.Meta {
	if len(a) == 0 || len(b) == 0 {
		return a
	}
	keep := metaSet(b)
	result := make([]parse.Meta, 0, len(a))
	for _, m := range a {
		if _, ok := keep[m]; !ok {
			result = append(result, m)
		}
	}
	return result
}
