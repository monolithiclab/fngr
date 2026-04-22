package internal

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	MetaKeyAuthor = "author"
	MetaKeyPeople = "people"
	MetaKeyTag    = "tag"
)

// Meta represents a key-value metadata pair attached to an event.
type Meta struct {
	Key   string
	Value string
}

var tagPatterns = []struct {
	re  *regexp.Regexp
	key string
}{
	{regexp.MustCompile(`@([\w][\w/\-]*)`), MetaKeyPeople},
	{regexp.MustCompile(`#([\w][\w/\-]*)`), MetaKeyTag},
}

// ParseBodyTags extracts #tag and @person shorthands from text.
// It returns @-tags (people) first, then #-tags (tag), with duplicates removed.
func ParseBodyTags(text string) []Meta {
	seen := make(map[Meta]struct{})
	var result []Meta

	for _, p := range tagPatterns {
		for _, m := range p.re.FindAllStringSubmatch(text, -1) {
			meta := Meta{Key: p.key, Value: m[1]}
			if _, ok := seen[meta]; !ok {
				seen[meta] = struct{}{}
				result = append(result, meta)
			}
		}
	}

	return result
}

// ParseFlagMeta parses --meta key=value flags into Meta slices.
func ParseFlagMeta(flags []string) ([]Meta, error) {
	if len(flags) == 0 {
		return nil, nil
	}

	result := make([]Meta, 0, len(flags))
	for _, f := range flags {
		key, value, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("invalid meta flag %q: missing '='", f)
		}
		result = append(result, Meta{Key: key, Value: value})
	}
	return result, nil
}

// CollectMeta combines metadata from all sources, deduplicating and preserving
// order: author, body tags (people before tag), flag metadata.
func CollectMeta(text string, flags []string, author string) ([]Meta, error) {
	seen := make(map[Meta]struct{})
	var result []Meta

	add := func(m Meta) {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			result = append(result, m)
		}
	}

	add(Meta{Key: MetaKeyAuthor, Value: author})

	for _, m := range ParseBodyTags(text) {
		add(m)
	}

	flagMeta, err := ParseFlagMeta(flags)
	if err != nil {
		return nil, err
	}
	for _, m := range flagMeta {
		add(m)
	}

	return result, nil
}

// BuildFTSContent constructs a full-text search content string from event text
// and metadata. The output is not meant to be parsed back.
func BuildFTSContent(text string, meta []Meta) string {
	parts := make([]string, 0, 1+len(meta))
	if text != "" {
		parts = append(parts, text)
	}
	for _, m := range meta {
		parts = append(parts, m.Key+"="+m.Value)
	}
	return strings.Join(parts, " ")
}
