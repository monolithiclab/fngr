package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/term"
)

// errPagerStartFailed signals that the pager process could not be started.
// Nothing is redirected when it fires, so the buffer keeps writing where it
// already pointed.
var errPagerStartFailed = errors.New("pager start failed")

// withPager pipes Out through the user's pager, when stdout is a TTY and
// disabled is false, and returns a closer the caller MUST defer.
//
// It redirects the buffer ioStreams already carries (see output.go) rather than
// installing one of its own: buffering is now unconditional and belongs to the
// stream, so all the pager changes is where the flushed bytes land. An Out that
// is not a *bufferedOut therefore gets no pager — in production it always is,
// and a test writing into a bytes.Buffer has no terminal to page to anyway.
//
// The closer flushes and returns any error from doing so: the bytes still held
// back are written nowhere else, so a failure there is a failure of the command
// rather than something to log past. The pager's own exit status is warned about
// instead, since a pager closed early is the user's decision and not a lost
// write. It waits for the pager either way, so output reaches the terminal
// before the process returns.
func withPager(s ioStreams, disabled bool) func() error {
	buf, ok := s.Out.(*bufferedOut)
	if !ok || disabled || !isTerminalWriter(buf.dest) {
		return noopCloser
	}
	cmd, in, err := newPagerCmd(buf.dest)
	if err != nil {
		fmt.Fprintf(s.Err, "warning: could not start pager: %v\n", err)
		return noopCloser
	}
	// Bound to the two values the closer uses rather than closing over s: the
	// whole struct would name the stdin reader in a closure that lives for the
	// length of the listing.
	errOut, restore := s.Err, buf.redirect(in)
	return func() error {
		err := restore()
		_ = in.Close()
		if werr := cmd.Wait(); werr != nil {
			fmt.Fprintf(errOut, "warning: pager exited with error: %v\n", werr)
		}
		return err
	}
}

// isTerminal is a var for test stubbing, like launchEditor: it is the only
// thing gating the pager branch and no test process has a terminal on stdout.
var isTerminal = term.IsTerminal

// isTerminalWriter reports whether w is a terminal. Only an *os.File can be
// one, so every pipe and every test buffer answers no without a syscall.
func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminalFile(f)
}

// isTerminalFile is the one fd-to-terminal question in the process, asked by
// isTerminalWriter for stdout and by main for stdin's IsTTY. One function so
// the stub seam and the overflow annotation exist once.
func isTerminalFile(f *os.File) bool {
	// #nosec G115 -- fd is a small int, cannot overflow
	return isTerminal(int(f.Fd()))
}

func noopCloser() error { return nil }

// newPagerCmd starts the user's pager writing to dest and returns the running
// command plus a writer connected to its stdin. $PAGER is tokenized by
// envCommand, which the editor launcher shares — see there for what the split
// does and does not interpret.
//
// dest rather than os.Stdout because dest is what the caller just established
// is a terminal, and it is where the buffer writes again once the pager is
// gone. They are the same file in every production run; taking the parameter
// is what keeps that an observation rather than two independent guesses.
func newPagerCmd(dest io.Writer) (*exec.Cmd, io.WriteCloser, error) {
	parts := pagerCommand()
	cmd := exec.Command(parts[0], parts[1:]...) // #nosec G204 -- pager comes from $PAGER, an explicit user choice.
	cmd.Stdout = dest
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errPagerStartFailed, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errPagerStartFailed, err)
	}
	return cmd, in, nil
}

func pagerCommand() []string {
	if argv := envCommand("PAGER"); len(argv) > 0 {
		return argv
	}
	return []string{"less", "-FRX"}
}
