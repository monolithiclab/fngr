package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// confirm writes prompt to out, reads a line from in, and returns true when
// the user confirms. An empty answer (just enter) returns defaultVal.
func confirm(in io.Reader, out io.Writer, prompt string, defaultVal bool) (bool, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.TrimSpace(strings.ToLower(answer)) {
	case "":
		return defaultVal, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
