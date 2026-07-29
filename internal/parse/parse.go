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

// Token renders m the way it appears inside the FTS index. Both sides of
// search have to agree on this: FTSContent writes it, and the -S filter
// parser (internal/event/filter.go) emits the same form to match it. A drift
// between the two would show up only as queries silently matching nothing.
func (m Meta) Token() string { return m.Key + "=" + m.Value }

// metaNamePattern is the character class accepted for @person / #tag names
// and for keys in `key=value` arguments: any Unicode letter or digit, plus
// '_', '/' and '-', starting with a letter, digit or '_'.
//
// The Unicode classes are load-bearing, not decoration. Go's regexp reads
// \w as ASCII-only, so it stopped at the first non-ASCII byte and silently
// truncated names — `@josé` was stored as `people=jos`, colliding with
// `@josa`. Don't reintroduce \w here.
const metaNamePattern = `[\p{L}\p{N}_][\p{L}\p{N}_/\-]*`

// metaNameBoundary must precede a @ or # for it to open a tag. Without it the
// sigils fire mid-token, so `bob@example.com` minted `people=example` and
// `.../guide#installation` minted `tag=installation`. Non-capturing, so the
// name stays at submatch index 1.
const metaNameBoundary = `(?:^|[^\p{L}\p{N}_])`

// MetaNameRule states metaNamePattern in prose for error messages, so the
// rule and its explanation can't drift apart. Exported because
// `cmd/fngr/meta.go::parseMetaFilter` reports the same rule.
const MetaNameRule = "letters, digits, '_', '/' or '-'"

var tagPatterns = []struct {
	re  *regexp.Regexp
	key string
}{
	{regexp.MustCompile(metaNameBoundary + `@(` + metaNamePattern + `)`), "people"},
	{regexp.MustCompile(metaNameBoundary + `#(` + metaNamePattern + `)`), "tag"},
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
// Names following @ or # must match metaNamePattern. Any other shape is
// rejected with the message "expected @person, #tag, or key=value".
func MetaArg(s string) (Meta, error) {
	if len(s) == 0 {
		return Meta{}, fmt.Errorf("expected @person, #tag, or key=value, got empty arg")
	}
	switch s[0] {
	case '@':
		name := s[1:]
		if !MetaNameRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid @person arg %q: name must be %s", s, MetaNameRule)
		}
		return Meta{Key: "people", Value: name}, nil
	case '#':
		name := s[1:]
		if !MetaNameRe.MatchString(name) {
			return Meta{}, fmt.Errorf("invalid #tag arg %q: name must be %s", s, MetaNameRule)
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
		if key == "" {
			return nil, fmt.Errorf("invalid --meta flag %q: empty key", f)
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
// This is the only definition of the formula. migrations/3.sql once carried
// a SQL transliteration of it, free to drift; migration 4 overwrites every
// row that produced and rebuilds through this function instead.
func FTSContent(title, body string, meta []Meta) string {
	parts := make([]string, 0, 2+len(meta))
	if title != "" {
		parts = append(parts, title)
	}
	if body != "" {
		parts = append(parts, body)
	}
	for _, m := range meta {
		parts = append(parts, m.Token())
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
