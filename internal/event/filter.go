package event

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/monolithiclab/fngr/internal/parse"
)

// ErrFilter is returned for a malformed -S expression. Callers use it to tell
// a user typo from a real query failure.
var ErrFilter = errors.New("invalid filter syntax")

// The -S grammar. `!` binds tighter than `&`, which binds tighter than `|`.
// Adjacent terms are an implicit AND, so `deploy staging` means both. There is
// no grouping — see the note on filterMatch below for why adding it would be
// cheap now.
//
//	expr  := or
//	or    := and ('|' and)*
//	and   := unary (('&')? unary)*
//	unary := '!' unary | TERM
//	TERM  := <any run of non-space, non-'&', non-'|' runes> '*'?
//
// This is a parser rather than the string substitution it replaced because
// negation cannot be expressed by rewriting tokens in place: FTS5 has no unary
// NOT, so `!a & b` has to become SQL boolean logic, not an FTS5 MATCH string.
// The old code emitted "NOT a AND b" and then stripped the leading NOT, which
// silently evaluated `!(a & b)` — the same query answered differently
// depending on operand order.

// filterMatch is the SQL for one term. Every term gets its own MATCH subquery
// so that `&`, `|` and `!` are plain SQL operators over sets of ids, which is
// what makes arbitrary nesting correct — and what would make grouping parens
// cheap to add (a `'(' expr ')'` case in parseUnary, plus a positive
// "does this token start an operand" test in parseAnd's stop condition).
const filterMatch = `e.id IN (SELECT rowid FROM events_fts WHERE events_fts MATCH ?)`

// ValidateFilter reports whether expr is a well-formed -S expression.
//
// Streaming callers must use this before writing anything. ListSeq compiles
// the filter too, but only once iteration has begun — by which point the JSON
// renderer has emitted its "[" and the CSV renderer its header row, so a pure
// syntax error would produce output *and* a non-zero exit.
func ValidateFilter(expr string) error {
	_, _, err := compileFilter(expr)
	return err
}

// compileFilter turns a -S expression into a SQL boolean condition over `e.id`
// plus the MATCH arguments to bind, in order.
func compileFilter(expr string) (string, []any, error) {
	p := &filterParser{toks: tokenizeFilter(expr), end: len([]rune(expr)) + 1}
	if len(p.toks) == 0 {
		return "", nil, fmt.Errorf("%w: expression is empty", ErrFilter)
	}
	return p.parseOr()
}

type tokenKind int

const (
	tokTerm tokenKind = iota
	tokAnd
	tokOr
	tokNot
)

type token struct {
	kind tokenKind
	text string
	pos  int // 1-based rune offset, for error messages
}

// tokenizeFilter splits expr into terms and operators. `&` and `|` always
// separate; `!` only starts an operator, so `!daily` negates but `wow!`
// searches for itself.
func tokenizeFilter(expr string) []token {
	runes := []rune(expr)
	var toks []token
	for i := 0; i < len(runes); {
		switch c := runes[i]; {
		case unicode.IsSpace(c):
			i++
		case c == '&':
			toks = append(toks, token{tokAnd, "&", i + 1})
			i++
		case c == '|':
			toks = append(toks, token{tokOr, "|", i + 1})
			i++
		case c == '!':
			toks = append(toks, token{tokNot, "!", i + 1})
			i++
		default:
			start := i
			for i < len(runes) && !isFilterDelim(runes[i]) {
				i++
			}
			toks = append(toks, token{tokTerm, string(runes[start:i]), start + 1})
		}
	}
	return toks
}

// isFilterDelim reports whether r ends a term. '!' is absent on purpose: a
// term already under way absorbs it.
func isFilterDelim(r rune) bool {
	return unicode.IsSpace(r) || r == '&' || r == '|'
}

type filterParser struct {
	toks []token
	i    int
	end  int // rune position just past the expression, for end-of-input errors
}

func (p *filterParser) peek() (token, bool) {
	if p.i >= len(p.toks) {
		return token{}, false
	}
	return p.toks[p.i], true
}

func (p *filterParser) parseOr() (string, []any, error) {
	sql, args, err := p.parseAnd()
	if err != nil {
		return "", nil, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind != tokOr {
			return sql, args, nil
		}
		p.i++
		rhs, rargs, err := p.parseAnd()
		if err != nil {
			return "", nil, err
		}
		sql = "(" + sql + " OR " + rhs + ")"
		args = append(args, rargs...)
	}
}

func (p *filterParser) parseAnd() (string, []any, error) {
	sql, args, err := p.parseUnary()
	if err != nil {
		return "", nil, err
	}
	for {
		t, ok := p.peek()
		if !ok || t.kind == tokOr {
			return sql, args, nil
		}
		if t.kind == tokAnd {
			p.i++
		} // else: adjacent terms, implicit AND
		rhs, rargs, err := p.parseUnary()
		if err != nil {
			return "", nil, err
		}
		sql = "(" + sql + " AND " + rhs + ")"
		args = append(args, rargs...)
	}
}

func (p *filterParser) parseUnary() (string, []any, error) {
	t, ok := p.peek()
	if !ok {
		return "", nil, fmt.Errorf("%w: expected a search term at position %d", ErrFilter, p.end)
	}
	switch t.kind {
	case tokNot:
		p.i++
		sql, args, err := p.parseUnary()
		if err != nil {
			return "", nil, err
		}
		return "(NOT " + sql + ")", args, nil
	case tokTerm:
		p.i++
		match, err := ftsTerm(t)
		if err != nil {
			return "", nil, err
		}
		return filterMatch, []any{match}, nil
	default:
		return "", nil, fmt.Errorf("%w: unexpected %q at position %d, expected a search term",
			ErrFilter, t.text, t.pos)
	}
}

// ftsTerm renders one term as an FTS5 MATCH expression. Terms are always
// quoted so that punctuation the FTS5 parser would otherwise choke on —
// hyphens in "session-handler", a stray double quote — is treated as text.
// A trailing '*' is kept outside the quotes as a prefix search; the len check
// keeps a lone "*" searchable as itself rather than emitting an empty phrase.
//
// The `@person` / `#tag` shorthands go through parse.MetaArg so that a name
// the rest of the CLI would reject is rejected here too. Resolving them
// locally instead let `-S '@bob@example.com'` compile to a phrase that can
// never match, so the search reported "no results" for input `event tag` and
// `meta -S` both call invalid.
func ftsTerm(t token) (string, error) {
	text, star := t.text, ""
	if len(text) > 1 && strings.HasSuffix(text, "*") {
		text, star = text[:len(text)-1], "*"
	}
	if text[0] == '@' || text[0] == '#' {
		m, err := parse.MetaArg(text)
		if err != nil {
			return "", fmt.Errorf("%w: %s (at position %d)", ErrFilter, err, t.pos)
		}
		text = m.Token()
	}
	return ftsQuote(text) + star, nil
}

func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
