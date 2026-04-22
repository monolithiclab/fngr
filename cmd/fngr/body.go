package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// errCancel signals a deliberate user cancel (empty editor save). AddCmd.Run
// recognises it and converts to (nil error + status 0).
var errCancel = errors.New("cancelled")

// launchEditor is overridable so tests can stub the editor exec without
// shelling out. Task 3 replaces this with realLaunchEditor.
var launchEditor = func(initial string) (string, error) {
	return "", errors.New("editor not yet implemented")
}

// resolveBody applies the body-source dispatch table from the spec
// (docs/superpowers/specs/2026-04-20-add-body-input-modes-design.md).
// Returns the body string or an error. errCancel signals a deliberate
// editor cancel.
func resolveBody(args []string, useEditor bool, io ioStreams) (string, error) {
	hasArgs := len(args) > 0
	piped := !io.IsTTY

	switch {
	case hasArgs && piped:
		return "", fmt.Errorf("ambiguous: body via both args and stdin; pick one")
	case !hasArgs && useEditor && piped:
		return "", fmt.Errorf("--edit conflicts with piped stdin")
	case hasArgs && useEditor:
		return launchEditor(strings.Join(args, " "))
	case hasArgs:
		return strings.Join(args, " "), nil
	case useEditor:
		return launchEditor("")
	case piped:
		return readStdin(io.In)
	default:
		return launchEditor("")
	}
}

func readStdin(in io.Reader) (string, error) {
	raw, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return "", fmt.Errorf("event text cannot be empty")
	}
	return body, nil
}
