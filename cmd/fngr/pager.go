package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// errPagerStartFailed signals that the pager process could not be started.
// Callers fall back to direct stdout when this fires.
var errPagerStartFailed = errors.New("pager start failed")

// outBufSize is the write buffer `fngr` list output goes through. Rendering
// wrote one line per syscall — 250 000 of them for a 250k list, 10-19% of the
// wall clock for tree, flat, markdown and JSON (CSV is the exception:
// csv.Writer buffers on its own). 16 KiB rather than the usual 64 KiB because
// the buffer is also the latency floor for `fngr | head -3`: a reader that
// wants three lines waits for the first full buffer, and 16 KiB of event lines
// is far more than any pager or `head` shows at once.
const outBufSize = 16 << 10

// withPager returns an ioStreams whose Out is buffered — and, when stdout is a
// TTY and disabled is false, piped through the user's pager — plus a closer the
// caller MUST defer.
//
// The closer flushes and returns any error from doing so: the tail of the
// output is written nowhere else, so a failure there is a failure of the
// command rather than something to log past. The pager's own exit status is
// warned about instead, since a pager closed early is the user's decision and
// not a lost write. It waits for the pager either way, so output reaches the
// terminal before the process returns.
func withPager(io ioStreams, disabled bool) (ioStreams, func() error) {
	// Bound at three words rather than closing over io: the whole struct would
	// keep the stdin reader reachable for the life of the listing.
	errOut := io.Err
	out, closePager := pagerWriter(io.Out, errOut, disabled)
	buf := bufio.NewWriterSize(out, outBufSize)
	io.Out = buf
	return io, func() error {
		err := buf.Flush()
		if cerr := closePager(); cerr != nil {
			fmt.Fprintf(errOut, "warning: pager exited with error: %v\n", cerr)
		}
		return err
	}
}

// isTerminal is a var for test stubbing, like launchEditor: it is the only
// thing gating the pager branch and no test process has a terminal on stdout.
var isTerminal = term.IsTerminal

// pagerWriter returns the writer list output should ultimately reach, plus a
// closer for it. The pager is skipped — and out handed back unchanged with a
// no-op closer — when disabled is true, when out is not a terminal, or when the
// pager fails to start, the one of the three worth a warning.
func pagerWriter(out, errOut io.Writer, disabled bool) (io.Writer, func() error) {
	// #nosec G115 -- fd is a small int, cannot overflow
	if f, ok := out.(*os.File); disabled || !ok || !isTerminal(int(f.Fd())) {
		return out, noopCloser
	}
	cmd, in, err := newPagerCmd()
	if err != nil {
		fmt.Fprintf(errOut, "warning: could not start pager: %v\n", err)
		return out, noopCloser
	}
	return in, func() error {
		_ = in.Close()
		return cmd.Wait()
	}
}

func noopCloser() error { return nil }

// newPagerCmd starts the user's pager and returns the running command plus
// a writer connected to its stdin. Tokenization of $PAGER is by space; a
// $PAGER value with spaces inside quotes is not supported (consistent with
// the spec).
func newPagerCmd() (*exec.Cmd, io.WriteCloser, error) {
	parts := pagerCommand()
	cmd := exec.Command(parts[0], parts[1:]...) // #nosec G204 -- pager comes from $PAGER, an explicit user choice.
	cmd.Stdout = os.Stdout
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
	if s := strings.TrimSpace(os.Getenv("PAGER")); s != "" {
		return strings.Fields(s)
	}
	return []string{"less", "-FRX"}
}
