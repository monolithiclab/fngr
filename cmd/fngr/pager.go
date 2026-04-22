package main

import (
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

// withPager returns an ioStreams whose Out is the stdin of the user's pager
// (if stdout is a TTY and disabled is false) plus a closer the caller MUST
// defer. The closer waits for the pager to exit so output flushes before
// the process returns.
//
// When stdout isn't a TTY, when disabled is true, when io.Out isn't an
// *os.File, or when the pager fails to start, withPager logs (only on
// genuine start failure) and returns the original io with a no-op closer.
func withPager(io ioStreams, disabled bool) (ioStreams, func() error) {
	if disabled {
		return io, noopCloser
	}
	f, ok := io.Out.(*os.File)
	if !ok {
		return io, noopCloser
	}
	if !term.IsTerminal(int(f.Fd())) { // #nosec G115 -- fd is a small int, cannot overflow
		return io, noopCloser
	}
	cmd, in, err := newPagerCmd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not start pager: %v\n", err)
		return io, noopCloser
	}
	return ioStreams{In: io.In, Out: in, Err: io.Err, IsTTY: io.IsTTY}, func() error {
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
