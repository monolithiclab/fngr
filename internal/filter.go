package internal

import (
	"strings"
)

// PreprocessFilter converts a user-facing filter expression into an FTS5 MATCH
// query string. It expands shorthands (#tag, @person), quotes key=value terms,
// and translates &, |, ! into AND, OR, NOT operators.
func PreprocessFilter(expr string) string {
	tokens := tokenizeFilter(expr)
	if len(tokens) == 0 {
		return ""
	}

	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		switch tok {
		case "&":
			out = append(out, "AND")
		case "|":
			out = append(out, "OR")
		default:
			out = append(out, convertTerm(tok))
		}
	}

	return strings.Join(out, " ")
}

// convertTerm transforms a single token into its FTS5 representation.
// It handles the ! prefix (NOT), # shorthand (tag), @ shorthand (people),
// key=value quoting, and bare word passthrough.
func convertTerm(tok string) string {
	// Handle NOT prefix: strip the ! and recurse, then prepend NOT.
	if strings.HasPrefix(tok, "!") {
		inner := convertTerm(tok[1:])
		return "NOT " + inner
	}

	// Hash tag shorthand: #value -> "tag=value"
	if strings.HasPrefix(tok, "#") {
		return `"` + MetaKeyTag + "=" + tok[1:] + `"`
	}

	// At tag shorthand: @value -> "people=value"
	if strings.HasPrefix(tok, "@") {
		return `"` + MetaKeyPeople + "=" + tok[1:] + `"`
	}

	// Key=value: quote the whole term for FTS5 phrase matching.
	if strings.Contains(tok, "=") {
		return `"` + tok + `"`
	}

	// Bare word: pass through unquoted.
	return tok
}

// tokenizeFilter splits a filter expression into tokens. It splits on spaces
// and treats & and | as standalone operator tokens. The ! prefix stays attached
// to the following term rather than becoming a separate token.
func tokenizeFilter(expr string) []string {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil
	}

	var tokens []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}

	for i := 0; i < len(expr); i++ {
		ch := expr[i]
		switch ch {
		case ' ':
			flush()
		case '&', '|':
			flush()
			tokens = append(tokens, string(ch))
		default:
			current.WriteByte(ch)
		}
	}
	flush()

	return tokens
}
