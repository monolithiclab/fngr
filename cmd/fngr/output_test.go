package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestNewBufferedOut_HoldsWritesUntilFlushed(t *testing.T) {
	t.Parallel()
	var dest bytes.Buffer
	out := newBufferedOut(&dest)

	if _, err := io.WriteString(out, "one line\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := dest.String(); got != "" {
		t.Errorf("dest = %q before the flush, want it held back", got)
	}
	if err := flushOut(out); err != nil {
		t.Fatalf("flushOut: %v", err)
	}
	if got := dest.String(); got != "one line\n" {
		t.Errorf("dest = %q after the flush, want %q", got, "one line\n")
	}
}

// TestNewBufferedOut_BuffersToTheDocumentedSize pins the one number: 16 KiB is
// a trade between syscalls and how long `fngr | head -3` waits for its first
// line, so a change to it is a change to that trade and not a tidy-up.
func TestNewBufferedOut_BuffersToTheDocumentedSize(t *testing.T) {
	t.Parallel()
	if got := newBufferedOut(io.Discard).available(); got != outBufSize {
		t.Errorf("buffer size = %d, want %d", got, outBufSize)
	}
}

func TestFlushOut_IsANoOpForAWriterThatDoesNotBuffer(t *testing.T) {
	t.Parallel()
	if err := flushOut(&bytes.Buffer{}); err != nil {
		t.Errorf("flushOut(bytes.Buffer) = %v, want nil", err)
	}
}

// TestFlushOut_ReportsTheError is why every caller checks it: with the stream
// buffered, a failed flush is output nobody received, and reporting success
// over it is the exact failure mode the buffer introduced.
func TestFlushOut_ReportsTheError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("disk full")
	out := newBufferedOut(errWriter{err: wantErr})

	if _, err := io.WriteString(out, "short line\n"); err != nil {
		t.Fatalf("write should be buffered, not attempted: %v", err)
	}
	if err := flushOut(out); !errors.Is(err, wantErr) {
		t.Errorf("flushOut = %v, want %v", err, wantErr)
	}
}

// TestBufferedOut_Redirect covers the swap the pager makes: nothing written for
// one destination may reach the other, in either direction, and the restore
// puts the original back.
func TestBufferedOut_Redirect(t *testing.T) {
	t.Parallel()
	var dest, elsewhere bytes.Buffer
	out := newBufferedOut(&dest)

	_, _ = io.WriteString(out, "before\n")
	restore := out.redirect(&elsewhere) // flushes "before" to dest
	_, _ = io.WriteString(out, "during\n")
	if err := restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	_, _ = io.WriteString(out, "after\n")
	if err := flushOut(out); err != nil {
		t.Fatalf("flushOut: %v", err)
	}

	if got, want := elsewhere.String(), "during\n"; got != want {
		t.Errorf("redirected dest = %q, want %q", got, want)
	}
	if got, want := dest.String(), "before\nafter\n"; got != want {
		t.Errorf("original dest = %q, want %q", got, want)
	}
}

// TestBufferedOut_RedirectReportsEitherFlush pins that the restore speaks for
// both halves. The first flush's error is the easy one to drop — it happens
// before the caller has anything to check — and it is bytes the old destination
// never got.
func TestBufferedOut_RedirectReportsEitherFlush(t *testing.T) {
	t.Parallel()
	firstErr := errors.New("stdout is gone")
	secondErr := errors.New("pager is gone")

	t.Run("the flush to the old destination", func(t *testing.T) {
		t.Parallel()
		out := newBufferedOut(errWriter{err: firstErr})
		_, _ = io.WriteString(out, "before\n")
		if err := out.redirect(&bytes.Buffer{})(); !errors.Is(err, firstErr) {
			t.Errorf("restore = %v, want %v", err, firstErr)
		}
	})

	t.Run("the flush to the new one", func(t *testing.T) {
		t.Parallel()
		out := newBufferedOut(&bytes.Buffer{})
		restore := out.redirect(errWriter{err: secondErr})
		_, _ = io.WriteString(out, "during\n")
		if err := restore(); !errors.Is(err, secondErr) {
			t.Errorf("restore = %v, want %v", err, secondErr)
		}
	})
}
