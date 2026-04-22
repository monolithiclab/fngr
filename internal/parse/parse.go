package parse

import (
	"fmt"
	"regexp"
	"strings"
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

// metaArgRe matches the body of an @person or #tag arg. Same character
// class as the body-tag patterns: word chars plus '/' and '-', starting
// with a word char.
var metaArgRe = regexp.MustCompile(`^[\w][\w/\-]*$`)

// MetaArg parses a single CLI argument into a Meta entry. Supported forms:
//
//	"@name"      -> {people, name}
//	"#name"      -> {tag, name}
//	"key=value"  -> {key, value}      (delegates to KeyValue)
//
// Names following @ or # must match the body-tag regex [\w][\w/\-]*. Any
// other shape is rejected with the message "expected @person, #tag, or
// key=value".
func MetaArg(s string) (Meta, error) {
	if len(s) == 0 {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got empty arg")
	}
	switch s[0] {
	case '@':
		name := s[1:]
		if !metaArgRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid @person arg %q: name must match [\\w][\\w/\\-]*", s)
		}
		return Meta{Key: "people", Value: name}, nil
	case '#':
		name := s[1:]
		if !metaArgRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid #tag arg %q: name must match [\\w][\\w/\\-]*", s)
		}
		return Meta{Key: "tag", Value: name}, nil
	}
	if !strings.Contains(s, "=") {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got %q", s)
	}
	key, value, err := KeyValue(s)
	if err != nil {
		return Meta{}, err
	}
	if key == "" {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got %q (empty key)", s)
	}
	return Meta{Key: key, Value: value}, nil
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
