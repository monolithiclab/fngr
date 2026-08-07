package main

import "fmt"

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
