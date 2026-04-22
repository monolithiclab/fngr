package event

import (
	"github.com/monolithiclab/fngr/internal/parse"
)

// Well-known meta keys. The matching value's domain meaning is encoded
// in the key, so renderers and CLI verbs can recognise them by name.
const (
	// MetaKeyAuthor identifies the user who recorded the event. Auto-set
	// from --author / $FNGR_AUTHOR / $USER; not renameable via meta verbs.
	MetaKeyAuthor = "author"
	// MetaKeyPeople holds names extracted from `@person` body shorthand.
	MetaKeyPeople = "people"
	// MetaKeyTag holds names extracted from `#tag` body shorthand.
	MetaKeyTag = "tag"
)

// CollectMeta merges the meta sources for a new event into one deduped
// slice in a deterministic order: the author tuple, then body-derived
// `@person` / `#tag` tags, then explicit `--meta key=value` flag entries.
// Errors out if any flag fails to parse as `key=value`.
func CollectMeta(text string, flags []string, author string) ([]parse.Meta, error) {
	seen := make(map[parse.Meta]struct{})
	var result []parse.Meta

	add := func(m parse.Meta) {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			result = append(result, m)
		}
	}

	add(parse.Meta{Key: MetaKeyAuthor, Value: author})

	for _, m := range parse.BodyTags(text) {
		add(m)
	}

	flagMeta, err := parse.FlagMeta(flags)
	if err != nil {
		return nil, err
	}
	for _, m := range flagMeta {
		add(m)
	}

	return result, nil
}
