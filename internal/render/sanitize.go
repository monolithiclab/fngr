package render

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Control characters are escaped rather than dropped. fngr is a journal: a
// note that arrived with a stray byte should still show what it contained,
// and `\x1b` on screen is both honest and harmless. Dropping the byte would
// silently rewrite the user's own text.
//
// The threat is output forgery, not code execution. A title carrying a
// newline prints as an extra line indistinguishable from a real event, and
// an ANSI/OSC sequence reaches the terminal untouched — `fngr event N` writes
// straight to the TTY with no pager in between.
//
// JSON and CSV are excluded on purpose, for different reasons. JSON escapes
// control bytes itself, and losslessly — an ESC comes back out of a decoder
// as an ESC — so a second pass would only break the
// `fngr --format=json | fngr add --format=json` round trip. CSV
// does *not* escape them — `csv.Writer` quotes for structural safety only —
// but CSV is a data export, and escaping there would be lossy: a reader
// could no longer tell a stored `\x1b` from an escaped one. Fidelity wins in
// the machine-readable formats; safety wins in the human-readable ones.

// SanitizeLine escapes every control character in s, newlines included. Use
// it wherever one event occupies exactly one output line, so stored text
// cannot introduce a line boundary the renderer did not intend.
//
// It is exported for `cmd/fngr`'s `meta` listing, which formats its own
// columns rather than going through this package.
func SanitizeLine(s string) string { return escapeControl(s, false) }

// sanitizeBlock escapes control characters but keeps newlines, for the
// contexts that lay text out over several lines on purpose — the body block
// of `fngr event N`.
func sanitizeBlock(s string) string { return escapeControl(s, true) }

// isControl reports whether r drives the terminal rather than printing:
// Unicode category Cc, which is C0 (U+0000–U+001F), DEL, and C1
// (U+0080–U+009F) — the last because a bare U+009B is a CSI introducer on
// some terminals. Tab always passes: it is whitespace, it cannot start an
// escape sequence, and bodies legitimately contain it.
func isControl(r rune, keepNewline bool) bool {
	switch r {
	case '\t':
		return false
	case '\n':
		return !keepNewline
	}
	return unicode.IsControl(r)
}

// escapeControl walks s by byte rather than by rune. `for _, r := range s`
// would hand a raw 0x9b over as utf8.RuneError — so the 8-bit CSI
// introducer, the very byte the C1 range exists to catch, would pass through
// untouched — and would rewrite every other malformed byte to U+FFFD, which
// is precisely the silent text-rewriting this file refuses to do. A byte
// that is not valid UTF-8 is escaped as itself.
//
// Text with nothing to escape — nearly all of it — is returned unchanged,
// having allocated nothing.
func escapeControl(s string, keepNewline bool) string {
	var b strings.Builder
	clean := 0 // s[clean:i] is still waiting to be copied verbatim

	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		invalid := r == utf8.RuneError && size == 1
		if !invalid && !isControl(r, keepNewline) {
			i += size
			continue
		}
		if invalid {
			r = rune(s[i])
		}

		if b.Len() == 0 {
			b.Grow(len(s) + 8)
		}
		b.WriteString(s[clean:i])
		writeEscape(&b, r)
		i += size
		clean = i
	}

	if clean == 0 {
		return s
	}
	b.WriteString(s[clean:])
	return b.String()
}

// writeEscape renders one control byte. `\n` and `\r` get their familiar
// spellings; everything else is `\xNN`, always two digits since nothing above
// U+009F reaches here.
//
// The nibbles are written by hand rather than with fmt.Fprintf: passing the
// builder as an io.Writer makes it escape to the heap, which costs an
// allocation on every call to escapeControl — including the overwhelmingly
// common one that has nothing to escape at all.
func writeEscape(b *strings.Builder, r rune) {
	const hex = "0123456789abcdef"
	switch r {
	case '\n':
		b.WriteString(`\n`)
	case '\r':
		b.WriteString(`\r`)
	default:
		b.WriteString(`\x`)
		b.WriteByte(hex[(r>>4)&0xf])
		b.WriteByte(hex[r&0xf])
	}
}
