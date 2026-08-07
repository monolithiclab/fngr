package parse

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
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

// EventText joins an event's title and body into the single string body-tag
// extraction runs over. Every caller of BodyTags that starts from a stored
// event must go through it: the add path, Update's sync and migration 5's
// back-fill each classify a tuple as body-derived or not, and a join that
// differs by so much as a space would have them disagree about a tag near the
// boundary — one stamping `source = 'body'`, another deleting it.
func EventText(title, body string) string { return title + " " + body }

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

// ErrEmptyKey is the one empty-key error, shared by the two functions that
// can reach that state from different directions — KeyValue, where `=v` cuts
// to nothing, and ValidateMeta, where a caller supplied it directly.
var ErrEmptyKey = errors.New("empty key")

// KeyValue splits s on the first '=' and returns the key and value. It
// returns an error if s does not contain '=' or the key is empty.
//
// Structural only, deliberately: KeyValue is on the path that *names* a
// tuple as well as the path that mints one, and `event untag 'k='` has to
// keep working on a row an older build stored. Storability is ValidateMeta's
// job, applied by the minting callers.
func KeyValue(s string) (key, value string, err error) {
	key, value, ok := strings.Cut(s, "=")
	if !ok {
		// Unquoted: every caller wraps this with %q of the same string, and
		// the pair used to be named twice in one sentence.
		return "", "", errors.New("missing '='")
	}
	if key == "" {
		return "", "", ErrEmptyKey
	}
	return key, value, nil
}

// ValidateMeta rejects a key/value pair fngr can store but not find again.
// Every path that *mints* one goes through it — `--meta`, `event tag`,
// `meta rename`'s new value, the `--format=json` `meta` array — because a
// tuple only one of them refuses is a tuple the others can still create. This
// comment is the canonical statement of the rule; the other sites point here.
//
// Only the minting paths. A verb that names an existing row — `event untag`,
// `meta delete`, `meta rename`'s old value — must stay able to say what an
// older build wrote, or the rows this rule exists to stop being created
// become the rows nothing can remove.
//
// A key must be exactly one -S term: not empty, no rune that ends a term, and
// not opening with the one that negates it. `-m 'a b=c d'` stored a row
// `-S 'a b=c d'` read as three terms and matched nothing, `-m 'a&b=c'` one it
// read as two, and `-m '!k=v'` one `-S '!k=v'` answered with every event
// *except* the tagged one — the silent complement of what was typed. Hence
// IsFilterDelim rather than a hand-listed set: the tokenizer is the authority
// on what it cannot read back, and a second copy of that list had already
// missed `&` and `|`.
//
// Nothing narrower: `-m ticket.id=PROJ-42` is a key MetaNameRe would refuse
// and is perfectly findable, so the rule is one *term*, not one *name*.
//
// A value may not be empty. `-m 'k='` used to render as `k=  (1)` — an entry
// that says nothing, which no verb but `event untag 'k='` could then name.
// Whitespace inside a value is allowed: `author=Ada Lovelace` is what people
// mean to write, `fngr meta -S` matches it exactly, and `-S author=Ada`
// finds it — a term ends at the space, but `key=firstword` is enough to reach
// the row, which is not true of a *key* split down the middle.
func ValidateMeta(key, value string) error {
	switch {
	case key == "":
		return ErrEmptyKey
	case strings.ContainsFunc(key, IsFilterDelim):
		return fmt.Errorf("key %q contains a space, '&' or '|', which -S reads as a term separator", key)
	case strings.HasPrefix(key, "!"):
		return fmt.Errorf("key %q opens with '!', which -S reads as negation", key)
	case value == "":
		return fmt.Errorf("empty value for key %q", key)
	}
	return nil
}

// IsFilterDelim reports whether r ends a term in an -S filter expression.
// The tokenizer in internal/event/filter.go is its structural user; it lives
// here because ValidateMeta has to refuse exactly what that tokenizer cannot
// read back, and internal/event imports parse rather than the reverse.
//
// '!' is absent on purpose: a term already under way absorbs it, so it ends
// nothing. Only a *leading* '!' negates, which ValidateMeta tests
// separately.
func IsFilterDelim(r rune) bool {
	return unicode.IsSpace(r) || r == '&' || r == '|'
}

// MetaNameRe matches a single @person / #tag name in isolation (no
// surrounding chars). Anchored form of the same character class used by the
// body-tag patterns, so a name typed on the command line is one the same text
// in a body would have yielded.
//
// It is deliberately *not* the rule for a `key=value` key — that is
// ValidateMeta, which is looser. A sigil name has to be a clean token
// because the body patterns have to find it unaided in running prose; a key
// the user spells out in full does not.
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
		return Meta{}, fmt.Errorf("invalid meta arg %q: %w", s, err)
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
			return nil, fmt.Errorf("invalid --meta flag %q: %w", f, err)
		}
		// --meta only ever mints, never names an existing row, so the
		// storability rule applies here and not in KeyValue itself.
		if err := ValidateMeta(key, value); err != nil {
			return nil, fmt.Errorf("invalid --meta flag %q: %w", f, err)
		}
		result = append(result, Meta{Key: key, Value: value})
	}
	return result, nil
}

// FTSColumns builds the two searchable strings indexed in events_fts: the
// event's own text, and its metadata as "key=value" tokens.
//
// They are separate columns because they used to be one, and a body is not
// trustworthy input: `fngr add "mentions secret=classified"` wrote a token
// indistinguishable from the row `-m secret=classified` writes, so a note
// could put itself into any tag view or (with `!`) out of one. The filter
// parser scopes every term to one column or the other, which is what makes
// the two kinds of match tell apart.
//
// Empty title or body contribute nothing (no leading/trailing or doubled
// spaces). The FTS tokenizer treats '=' as a token char, so `tag=ops` is one
// token and a -S '#ops' filter matches it whole.
//
// This is the only definition of the live formula, and both columns are
// stated here rather than in two functions so a caller cannot write one and
// forget the other. migrations/3.sql once carried a SQL transliteration of the
// single-column form it replaced, free to drift; migration 4 overwrote every
// row that produced, and migration 6 rebuilds every row again through this.
// The pre-6 one-column formula lives on in db.legacyFTSContent, frozen beside
// the migration that still writes it.
func FTSColumns(title, body string, meta []Meta) (content, metaTokens string) {
	tokens := make([]string, len(meta))
	for i, m := range meta {
		tokens[i] = m.Token()
	}
	return joinNonEmpty(title, body), strings.Join(tokens, " ")
}

// joinNonEmpty joins the two parts with a single space, skipping an empty one
// so the result never carries a leading, trailing or doubled space.
func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + " " + b
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
