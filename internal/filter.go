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

	result := strings.Join(out, " ")
	// FTS5's NOT is a binary operator (a NOT b), so "AND NOT" is redundant.
	result = strings.ReplaceAll(result, " AND NOT ", " NOT ")
	return result
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

	if strings.HasPrefix(tok, "#") {
		return ftsQuote(MetaKeyTag + "=" + tok[1:])
	}

	if strings.HasPrefix(tok, "@") {
		return ftsQuote(MetaKeyPeople + "=" + tok[1:])
	}

	if strings.Contains(tok, "=") {
		return ftsQuote(tok)
	}

	return tok
}

func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
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

	for _, ch := range expr {
		switch ch {
		case ' ':
			flush()
		case '&', '|':
			flush()
			tokens = append(tokens, string(ch))
		default:
			current.WriteRune(ch)
		}
	}
	flush()

	return tokens
}
