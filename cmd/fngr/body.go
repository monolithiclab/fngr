package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// errCancel signals a deliberate user cancel (empty editor save). AddCmd.Run
// recognises it and converts to (nil error + status 0).
var errCancel = errors.New("cancelled")

// launchEditor is overridable so tests can stub the editor exec without
// shelling out. Production wires it to realLaunchEditor.
var launchEditor = realLaunchEditor

// resolveBody applies the body-source dispatch table from the spec
// (docs/superpowers/specs/2026-04-20-add-body-input-modes-design.md).
// Returns the body string or an error. errCancel signals a deliberate
// editor cancel.
func resolveBody(args []string, useEditor bool, io ioStreams) (string, error) {
	hasArgs := len(args) > 0

	// "piped" means stdin actually carries a body, not merely that stdin is
	// non-interactive. A script, cron job, or CI step runs with stdin bound to
	// /dev/null (non-TTY, no data); treating that as a piped body broke plain
	// `fngr add "note"`. Only peek when non-TTY — peeking a real terminal would
	// block waiting for the user to type.
	in := io.In
	piped := false
	if !io.IsTTY {
		piped, in = peekHasData(io.In)
	}

	switch {
	case hasArgs && piped:
		return "", fmt.Errorf("ambiguous: body via both args and stdin; pick one")
	case !hasArgs && useEditor && piped:
		return "", fmt.Errorf("--edit conflicts with piped stdin")
	case hasArgs && useEditor:
		return launchEditor(strings.Join(args, " "))
	case hasArgs:
		body := strings.Join(args, " ")
		if strings.TrimSpace(body) == "" {
			return "", fmt.Errorf("event text cannot be empty")
		}
		return body, nil
	case useEditor:
		return launchEditor("")
	case piped:
		return readStdin(in)
	case io.IsTTY:
		// Bare interactive `fngr add` opens the editor on an empty buffer.
		return launchEditor("")
	default:
		// Non-interactive with no args and nothing piped: no body source.
		return "", fmt.Errorf("event text cannot be empty")
	}
}

// peekHasData reports whether in carries at least one byte without consuming
// it, returning a reader that replays the peeked byte. Callers must use the
// returned reader for subsequent reads. Used to distinguish a genuinely piped
// body from a merely non-interactive stdin (a script's empty /dev/null).
func peekHasData(in io.Reader) (bool, io.Reader) {
	br := bufio.NewReader(in)
	if _, err := br.Peek(1); err != nil {
		return false, br // EOF or read error: no body to read.
	}
	return true, br
}

// maxStdinBytes caps stdin reads to bound memory when something large
// (or unbounded, e.g. `cat /dev/zero | fngr add`) gets piped in. Sized
// to comfortably fit a JSON batch import; raise if real workflows hit it.
const maxStdinBytes = 16 << 20 // 16 MiB

func readStdin(in io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(in, maxStdinBytes+1))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	if len(raw) > maxStdinBytes {
		return "", fmt.Errorf("stdin exceeds %d-byte limit", maxStdinBytes)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return "", fmt.Errorf("event text cannot be empty")
	}
	return body, nil
}

// realLaunchEditor opens the user's $VISUAL/$EDITOR on a temp file seeded
// with `initial`, waits for it to exit, and returns the trimmed contents.
// Empty save returns errCancel so callers can treat it as "user cancelled".
func realLaunchEditor(initial string) (string, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return "", fmt.Errorf("no editor configured: set $EDITOR or $VISUAL")
	}

	f, err := os.CreateTemp("", "fngr-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)

	if initial != "" {
		if _, err := f.WriteString(initial); err != nil {
			_ = f.Close() // best-effort; primary error already captured.
			return "", fmt.Errorf("write initial: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close temp file: %w", err)
	}

	cmd := exec.Command(editor, name) // #nosec G204,G702 -- editor comes from $VISUAL/$EDITOR, an explicit user choice.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor exited: %w", err)
	}

	raw, err := os.ReadFile(name) // #nosec G304 -- name is from os.CreateTemp, not user input.
	if err != nil {
		return "", fmt.Errorf("read temp file: %w", err)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return "", errCancel
	}
	return body, nil
}
