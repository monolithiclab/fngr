package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// errNoAnswer is returned when a prompt reaches end-of-input with nothing
// typed — there is no one there to answer.
var errNoAnswer = errors.New("no answer on stdin; re-run with --force to skip the prompt")

// confirm writes prompt to out, reads a line from in, and returns true when
// the user confirms. An empty answer (just enter) returns defaultVal; reaching
// EOF with nothing typed returns errNoAnswer.
func confirm(in io.Reader, out io.Writer, prompt string, defaultVal bool) (bool, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	trimmed := strings.TrimSpace(strings.ToLower(answer))
	// Silence is not consent — and `meta rename` defaults to yes, so a cron or
	// `</dev/null` run would otherwise rewrite metadata and report success.
	// The trimmed == "" half matters: ReadString also returns io.EOF for a
	// final line with no trailing newline, so a bare `y` piped without one
	// must still confirm.
	if errors.Is(err, io.EOF) && trimmed == "" {
		return false, errNoAnswer
	}
	switch trimmed {
	case "":
		return defaultVal, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
