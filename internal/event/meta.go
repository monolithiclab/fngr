package event

import (
	"github.com/monolithiclab/fngr/internal/parse"
)

const (
	MetaKeyAuthor = "author"
	MetaKeyPeople = "people"
	MetaKeyTag    = "tag"
)

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
