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

// metaNamePattern is the character class accepted for @person / #tag names
// and for keys in `key=value` arguments. Word chars plus '/' and '-',
// starting with a word char.
const metaNamePattern = `[\w][\w/\-]*`

var tagPatterns = []struct {
	re  *regexp.Regexp
	key string
}{
	{regexp.MustCompile(`@(` + metaNamePattern + `)`), "people"},
	{regexp.MustCompile(`#(` + metaNamePattern + `)`), "tag"},
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

// MetaNameRe matches a single @person / #tag name or a `key=value` key
// in isolation (no surrounding chars). Anchored form of the same character
// class used by the body-tag patterns; exported so callers outside parse
// (e.g. `cmd/fngr/meta.go::parseMetaFilter`) can validate bare-key input
// without re-defining the rule.
var MetaNameRe = regexp.MustCompile(`^` + metaNamePattern + `$`)

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
		if !MetaNameRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid @person arg %q: name must match [\\w][\\w/\\-]*", s)
		}
		return Meta{Key: "people", Value: name}, nil
	case '#':
		name := s[1:]
		if !MetaNameRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid #tag arg %q: name must match [\\w][\\w/\\-]*", s)
		}
		return Meta{Key: "tag", Value: name}, nil
	}
	if !strings.Contains(s, "=") {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got %q", s)
	}
	key, value, err := KeyValue(s)
	if err != nil {
		return Meta{}, fmt.Errorf("parse meta arg %q: %w", s, err)
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

// FTSContent builds the searchable string indexed in events_fts. Empty
// title or body contribute nothing (no leading/trailing or doubled
// spaces). Meta entries render as "key=value" tokens — the FTS
// tokenizer treats '=' as a token char, so a -S '#ops' filter matches
// the literal "tag=ops" emitted here.
//
// The SQL rebuild query in internal/db/migrations/3.sql must match
// this formula. If you change one, change the other.
func FTSContent(title, body string, meta []Meta) string {
	parts := make([]string, 0, 2+len(meta))
	if title != "" {
		parts = append(parts, title)
	}
	if body != "" {
		parts = append(parts, body)
	}
	for _, m := range meta {
		parts = append(parts, m.Key+"="+m.Value)
	}
	return strings.Join(parts, " ")
}

// SplitTitleBody splits text on the first occurrence of ". " (dot
// followed by a space). The dot+space sequence is dropped; both sides
// are trimmed of surrounding whitespace. When no ". " appears the
// whole input becomes the title and the body is empty.
func SplitTitleBody(text string) (title, body string) {
	if before, after, ok := strings.Cut(text, ". "); ok {
		return strings.TrimSpace(before), strings.TrimSpace(after)
	}
	return strings.TrimSpace(text), ""
}
