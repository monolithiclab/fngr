package main

import (
	"bufio"
	"cmp"
	"io"
)

// outBufSize is the write buffer every byte of fngr's stdout goes through.
// Rendering wrote one line per syscall — 250 000 of them for a 250k list,
// 10-19% of the wall clock for tree, flat, markdown and JSON (CSV is the
// exception: csv.Writer buffers on its own). 16 KiB rather than the usual
// 64 KiB because the buffer is also the latency floor for `fngr | head -3`: a
// reader that wants three lines waits for the first full buffer, and 16 KiB of
// event lines is far more than any pager or `head` shows at once.
const outBufSize = 16 << 10

// bufferedOut is what main puts in ioStreams.Out. Buffering belongs to the
// stream, so it is installed once where the process's streams are built rather
// than by whichever command remembers to ask: it started inside withPager, and
// `list` was consequently the only command that had it — `fngr event N -t
// --format=json` wrote 2N+1 syscalls, +19% wall clock over the same rows
// buffered.
//
// The destination is swappable so the pager can be spliced in underneath a
// command already holding the stream, instead of stacking a second buffer on
// top of this one. dest is the sink w currently writes to, kept alongside
// because bufio.Writer does not expose its own: the pager needs the real file
// to ask whether it is a terminal, and to hand the child something to write to.
//
// The writer is a field rather than an embedded type so that Reset is not
// promoted onto bufferedOut. It is the one method that moves the sink without
// telling dest, and a stale dest points the pager at the wrong file — the whole
// invariant, publishable in one line by an embed nothing else needed.
type bufferedOut struct {
	w    *bufio.Writer
	dest io.Writer
}

func newBufferedOut(dest io.Writer) *bufferedOut {
	return &bufferedOut{w: bufio.NewWriterSize(dest, outBufSize), dest: dest}
}

func (o *bufferedOut) Write(p []byte) (int, error) { return o.w.Write(p) }

// WriteString forwards explicitly so io.WriteString still reaches
// bufio.Writer's own, rather than falling back to a []byte conversion per call.
func (o *bufferedOut) WriteString(s string) (int, error) { return o.w.WriteString(s) }

func (o *bufferedOut) Flush() error { return o.w.Flush() }

// available reports the unused capacity of an untouched buffer, for the one
// test that pins outBufSize.
func (o *bufferedOut) available() int { return o.w.Available() }

// point sends subsequent writes to w. The two assignments belong together —
// dest is what the pager reads to find the terminal, so a sink moved without it
// is the stale-dest bug the unexported writer exists to prevent.
func (o *bufferedOut) point(w io.Writer) {
	o.dest = w
	o.w.Reset(w)
}

// redirect sends everything written from here on to w, and returns the restore:
// flush to w, then point back at the destination redirect replaced. Both halves
// flush before switching, so no byte written for one destination can reach the
// other — bufio.Writer.Reset discards silently, and the whole point of the
// buffer is that its contents exist nowhere else.
//
// The restore reports the first error either flush saw, for the same reason: a
// flush that failed is output the user never received.
func (o *bufferedOut) redirect(w io.Writer) func() error {
	prev, prevErr := o.dest, o.Flush()
	o.point(w)
	return func() error {
		err := o.Flush()
		o.point(prev)
		return cmp.Or(prevErr, err)
	}
}

// flushOut flushes w if it buffers, and is a no-op otherwise. A type assertion
// rather than a field on ioStreams, because commands hold an io.Writer and a
// test hands them a bytes.Buffer: in production Out is always a *bufferedOut,
// and everywhere else there is nothing held back to flush.
func flushOut(w io.Writer) error {
	if f, ok := w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}
