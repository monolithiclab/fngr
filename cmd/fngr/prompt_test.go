package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestConfirm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    bool
		wantErr bool
	}{
		{name: "empty defaults to yes", input: "\n", want: true},
		{name: "y", input: "y\n", want: true},
		{name: "yes", input: "yes\n", want: true},
		{name: "uppercase Y", input: "Y\n", want: true},
		{name: "n", input: "n\n", want: false},
		{name: "no", input: "no\n", want: false},
		{name: "garbage", input: "maybe\n", want: false},
		// An answer with no trailing newline still comes back with io.EOF, so
		// the EOF check must look at what was typed, not just the error.
		{name: "y without newline", input: "y", want: true},
		{name: "n without newline", input: "n", want: false},
		// Nothing typed at all: no one is there to answer, so taking the
		// default would be inventing consent.
		{name: "eof with nothing typed", input: "", wantErr: true},
		{name: "eof after whitespace only", input: "  \t", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			got, err := confirm(strings.NewReader(tt.input), &out, "Continue? [Y/n] ", true)
			if out.String() != "Continue? [Y/n] " {
				t.Errorf("prompt not written; got %q", out.String())
			}
			if tt.wantErr {
				if !errors.Is(err, errNoAnswer) {
					t.Fatalf("err = %v, want errNoAnswer", err)
				}
				if got {
					t.Error("confirm returned true alongside an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if got != tt.want {
				t.Errorf("confirm(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestConfirm_IOErrors covers the two failure paths that are not "the user
// said no": a prompt that cannot be written and a stdin that cannot be read.
// Both must surface the error rather than fall through to defaultVal.
func TestConfirm_IOErrors(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")

	t.Run("prompt write fails", func(t *testing.T) {
		t.Parallel()
		got, err := confirm(strings.NewReader("y\n"), errWriter{err: wantErr}, "Continue? ", true)
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if got {
			t.Error("confirm returned true alongside an error")
		}
	})

	t.Run("stdin read fails", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		got, err := confirm(errReader{}, &out, "Continue? ", true)
		if err == nil {
			t.Fatal("err = nil, want the read error")
		}
		if got {
			t.Error("confirm returned true alongside an error")
		}
	})

	// A buffered Out is the only way to reach the flush: writing the prompt
	// through it cannot fail, so the error the user never saw surfaces here or
	// nowhere.
	t.Run("prompt flush fails", func(t *testing.T) {
		t.Parallel()
		got, err := confirm(strings.NewReader("y\n"), newBufferedOut(errWriter{err: wantErr}), "Continue? ", true)
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if got {
			t.Error("confirm returned true alongside an error")
		}
	})
}

// TestConfirm_FlushesThePromptBeforeReading is confirm's half of the buffered
// stdout contract: it blocks on stdin, so a prompt still sitting in the buffer
// is a question the user is never shown.
func TestConfirm_FlushesThePromptBeforeReading(t *testing.T) {
	t.Parallel()
	const prompt = "Continue? [y/N] "
	var dest bytes.Buffer

	in := checkedReader{t: t, dest: &dest, want: prompt, src: strings.NewReader("y\n")}
	got, err := confirm(in, newBufferedOut(&dest), prompt, false)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !got {
		t.Error("confirm = false, want true")
	}
}

// checkedReader fails the test unless dest already holds want by the time the
// first byte of the answer is asked for.
type checkedReader struct {
	t    *testing.T
	dest *bytes.Buffer
	want string
	src  io.Reader
}

func (r checkedReader) Read(p []byte) (int, error) {
	r.t.Helper()
	if got := r.dest.String(); got != r.want {
		r.t.Errorf("stdout = %q when the answer was read, want the prompt %q", got, r.want)
	}
	return r.src.Read(p)
}
