package main

import (
	"fmt"
	"io"
)

// reportNone says that a query matched nothing, in the one wording and on the
// one stream every listing uses: `No events found.`, `No metadata found.`
//
// stderr, not stdout, because it is a note about the result rather than a row
// of it — `fngr meta -S tag | wc -l` should count entries, not the sentence
// saying there were none. Shared rather than written per command so that the
// choice of stream is made once: it is the whole reason the message can be
// printed unconditionally, even for the formats that already say `[]`.
func reportNone(w io.Writer, noun string) {
	fmt.Fprintf(w, "No %s found.\n", noun)
}

// plural renders a count together with its noun, adding a regular "-s" for
// anything but one: `plural(1, "occurrence")` is "1 occurrence" and
// `plural(0, "event")` is "0 events".
//
// Only the regular form is handled, and deliberately so — every noun fngr
// counts (occurrence, event, record) takes a plain -s, and a helper carrying
// an irregular-plural argument would be a wider interface than any call site
// needs. A count of an irregular noun should say something else instead:
// `delete -r` reports the size of a subtree in events rather than in children.
func plural(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
