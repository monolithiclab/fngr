package parse

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	DateFormat     = "2006-01-02"
	DateTimeFormat = "2006-01-02 15:04:05"
)

type Meta struct {
	Key   string
	Value string
}

var tagPatterns = []struct {
	re  *regexp.Regexp
	key string
}{
	{regexp.MustCompile(`@([\w][\w/\-]*)`), "people"},
	{regexp.MustCompile(`#([\w][\w/\-]*)`), "tag"},
}

func BodyTags(text string) []Meta {
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

// KeyValue splits s on the first '=' and returns the key and value.
// It returns an error if s does not contain '='.
func KeyValue(s string) (key, value string, err error) {
	key, value, ok := strings.Cut(s, "=")
	if !ok {
		return "", "", fmt.Errorf("invalid key=value pair %q", s)
	}
	return key, value, nil
}

func FlagMeta(flags []string) ([]Meta, error) {
	if len(flags) == 0 {
		return nil, nil
	}

	result := make([]Meta, 0, len(flags))
	for _, f := range flags {
		key, value, err := KeyValue(f)
		if err != nil {
			return nil, fmt.Errorf("invalid --meta flag: %w", err)
		}
		result = append(result, Meta{Key: key, Value: value})
	}
	return result, nil
}

func FTSContent(text string, meta []Meta) string {
	parts := make([]string, 0, 1+len(meta))
	if text != "" {
		parts = append(parts, text)
	}
	for _, m := range meta {
		parts = append(parts, m.Key+"="+m.Value)
	}
	return strings.Join(parts, " ")
}
