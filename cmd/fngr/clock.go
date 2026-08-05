package main

import (
	"fmt"
	"io"
	"time"

	"github.com/monolithiclab/fngr/internal/timefmt"
)

// warnSkippedClock reports a wall clock the local zone does not have. A DST
// spring-forward skips an hour, and neither time.Date nor time.ParseInLocation
// says so: both resolve a clock inside the gap to the hour before it and return
// no error, so the command stored a timestamp an hour off what was asked for
// and reported success.
//
// A warning rather than a refusal — the shifted instant is a real one and
// almost certainly the intended entry, so failing would leave nothing useful to
// type instead. exists comes from timefmt: ParsePartial reports it for a clock
// the user typed, the splice helpers for one assembled from an existing
// timestamp. A caller whose input cannot be contradicted passes true and this
// is a no-op.
func warnSkippedClock(w io.Writer, exists bool, asked string, stored time.Time) {
	if exists {
		return
	}
	fmt.Fprintf(w, "warning: %q does not exist in %s (skipped by a daylight-saving change); stored %s\n",
		asked, stored.Location(), stored.Format(timefmt.DateTimeFormat))
}
