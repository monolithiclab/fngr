package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
)

// errCancel signals a deliberate user cancel (empty editor save). AddCmd.Run
// recognises it and converts to (nil error + status 0).
var errCancel = errors.New("cancelled")

// errEditNeedsTTY rejects -e when there is no terminal for the editor to run
// in. A sentinel rather than an inline fmt.Errorf so tests match it with
// errors.Is instead of by substring, same as errNoAnswer.
var errEditNeedsTTY = errors.New("--edit needs a terminal; stdin is not a TTY")

// launchEditor is overridable so tests can stub the editor exec without
// shelling out. Production wires it to realLaunchEditor.
var launchEditor = realLaunchEditor

// resolveBody applies the body-source dispatch table from the spec
// (docs/superpowers/specs/2026-04-20-add-body-input-modes-design.md).
// Returns the body string or an error. errCancel signals a deliberate
// editor cancel.
func resolveBody(args []string, useEditor bool, io ioStreams) (string, error) {
	switch {
	case useEditor && !io.IsTTY:
		// A capability check, not a content one: IsTTY is already known, so
		// asking it costs no read. See REVIEW.md H7 for what launching an
		// editor without a terminal did instead.
		return "", errEditNeedsTTY
	case len(args) > 0 && useEditor:
		return editBody(strings.Join(args, " "), io.Out)
	case len(args) > 0:
		body := strings.Join(args, " ")
		if strings.TrimSpace(body) == "" {
			return "", fmt.Errorf("event title cannot be empty")
		}
		return body, nil
	case io.IsTTY:
		// Bare interactive `fngr add`, or -e with no args: the guard above
		// has already established that -e implies a TTY, so this one case
		// covers both. Editor on an empty buffer either way.
		return editBody("", io.Out)
	default:
		// Nothing else can supply a body, so stdin is it. Reading it blocks
		// until EOF, and an open-but-idle pipe (`sleep 30 | fngr add`, a CI
		// runner's inherited stdin) never delivers one — so blocking belongs
		// only here, where the body is exactly what we are waiting for. Args
		// and -e already answer the question and must not consult stdin at
		// all. An empty /dev/null hits EOF at once and readStdin reports
		// `event title cannot be empty`.
		return readStdin(io.In)
	}
}

// editBody hands the terminal to $EDITOR, flushing stdout first. It is the
// other half of confirm's rule: fngr's stdout is buffered (see output.go), and
// anything still held back when a child takes the screen surfaces after the
// editor exits, or not at all if the editor clears it on the way out. Both
// editor branches of resolveBody go through here so neither can forget, and
// the flush error is returned rather than logged past for the reason every
// other flush error is — those bytes were written nowhere else.
//
// Nothing writes to Out before resolveBody today. That is a fact about
// AddCmd.Run's current statement order, not a property of the stream, and it
// is not the kind of fact a later warning line should be able to invalidate
// silently.
func editBody(initial string, out io.Writer) (string, error) {
	if err := flushOut(out); err != nil {
		return "", err
	}
	return launchEditor(initial)
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
		return "", fmt.Errorf("event title cannot be empty")
	}
	return body, nil
}

// realLaunchEditor opens the user's $VISUAL/$EDITOR on a temp file seeded
// with `initial`, waits for it to exit, and returns the trimmed contents.
// Empty save returns errCancel so callers can treat it as "user cancelled".
// It inherits os.Stdin, so callers must have established that a terminal
// exists — resolveBody's first branch is the only such caller.
func realLaunchEditor(initial string) (string, error) {
	argv := envCommand("VISUAL", "EDITOR")
	if len(argv) == 0 {
		return "", fmt.Errorf("no editor configured: set $EDITOR or $VISUAL")
	}

	// Above the temp file, not just around cmd.Run: defers are LIFO, so a
	// bracket registered later would be lifted *before* the removal below and
	// hand the one step this exists to protect back to the default disposition.
	defer ignoreTerminalSignals()()

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

	cmd := exec.Command(argv[0], append(slices.Clone(argv[1:]), name)...) // #nosec G204,G702 -- editor comes from $VISUAL/$EDITOR, an explicit user choice.
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

// ignoreTerminalSignals suspends fngr's response to the two signals a terminal
// delivers to the whole foreground process group — Ctrl-C is the one that
// matters, SIGQUIT rides along because it arrives the same way — and returns
// the restore. realLaunchEditor is the only caller.
//
// The editor is a child in that same group, so Ctrl-C reaches it too — and vim,
// like most editors, handles SIGINT itself and carries on. fngr's default
// disposition is to die, which left vim owning the terminal while the process
// that was going to read its file had already gone, taking the deferred
// temp-file removal with it. Letting the foreground program decide what the key
// means is what git does around its own editor launch.
//
// What it brackets is that temp file, not the exec: the invariant is "a file
// exists that only this process will unlink", which is why the caller registers
// it above os.CreateTemp and why the pager — which holds nothing a dead fngr
// would strand, and where Ctrl-C is the normal way to abandon a long listing —
// deliberately does not get the same treatment.
//
// Notify onto a buffered channel nobody reads, *not* signal.Ignore, and the two
// reasons are each disqualifying:
//
//   - signal.Reset does not undo signal.Ignore. Reset only lifts a Notify, so
//     an Ignore leaves the OS disposition at SIG_IGN for the life of the
//     process — the "restore" is a no-op and fngr never responds to Ctrl-C
//     again.
//   - exec preserves an *ignored* disposition where it resets a caught one, so
//     under Ignore the editor inherits SIG_IGN and cannot itself be
//     interrupted. That defeats the whole point for anything that does not
//     handle SIGINT on its own — including a wrapper script, which is exactly
//     what `EDITOR="code -w"` is.
//
// No drain goroutine: Notify never blocks sending, so one buffered slot absorbs
// the first signal and every later one is dropped on the floor. Dropped rather
// than deferred is the intent — the keystroke was aimed at the editor.
//
// SIGTERM and SIGKILL are still out of reach and always will be; the file they
// leave behind is mode 0600 in the per-user $TMPDIR.
func ignoreTerminalSignals() func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGQUIT)
	return func() { signal.Stop(ch) }
}
