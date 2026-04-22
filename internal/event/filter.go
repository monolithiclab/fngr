package event

import (
	"strings"
)

func preprocessFilter(expr string) string {
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
	result = strings.ReplaceAll(result, " AND NOT ", " NOT ")
	return result
}

var shorthandKeys = map[byte]string{
	'#': MetaKeyTag,
	'@': MetaKeyPeople,
}

func convertTerm(tok string) string {
	if strings.HasPrefix(tok, "!") {
		return "NOT " + convertTerm(tok[1:])
	}

	if key, ok := shorthandKeys[tok[0]]; ok {
		return ftsQuote(key + "=" + tok[1:])
	}

	if strings.Contains(tok, "=") {
		return ftsQuote(tok)
	}

	return tok
}

func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

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
