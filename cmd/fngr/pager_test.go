package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithPager_DisabledNoOps(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	io := ioStreams{In: strings.NewReader(""), Out: &out, Err: io.Discard, IsTTY: false}

	wrapped, closer := withPager(io, true)
	if wrapped.Out != &out {
		t.Errorf("wrapped.Out should be the original buffer when disabled")
	}
	if err := closer(); err != nil {
		t.Errorf("disabled closer returned error: %v", err)
	}
}

func TestWithPager_NonTTYOutNoOps(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	io := ioStreams{In: strings.NewReader(""), Out: &out, Err: io.Discard, IsTTY: false}

	wrapped, closer := withPager(io, false)
	if wrapped.Out != &out {
		t.Errorf("wrapped.Out should be the original buffer when Out has no fd")
	}
	if err := closer(); err != nil {
		t.Errorf("no-op closer returned error: %v", err)
	}
}

func TestWithPager_PipesToPagerProcess(t *testing.T) {
	dir := t.TempDir()
	captured := filepath.Join(dir, "captured.txt")

	// Fake pager: dump stdin to a file we can read.
	script := filepath.Join(dir, "fake-pager.sh")
	body := "#!/bin/sh\ncat > " + captured + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake pager: %v", err)
	}
	t.Setenv("PAGER", script)

	cmd, in, err := newPagerCmd()
	if err != nil {
		t.Fatalf("newPagerCmd: %v", err)
	}
	if _, err := io.WriteString(in, "hello pager\n"); err != nil {
		t.Fatalf("write to pager: %v", err)
	}
	if err := in.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("pager wait: %v", err)
	}
	got, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("read captured: %v", err)
	}
	if string(got) != "hello pager\n" {
		t.Errorf("captured = %q, want %q", string(got), "hello pager\n")
	}
}

func TestWithPager_PagerStartFailureSurfaces(t *testing.T) {
	t.Setenv("PAGER", "/no/such/pager-binary-that-cannot-exist-xyz")

	_, _, err := newPagerCmd()
	if !errors.Is(err, errPagerStartFailed) {
		t.Errorf("err = %v, want errPagerStartFailed", err)
	}
}

func TestPagerCommand_MultiTokenPagerEnv(t *testing.T) {
	t.Setenv("PAGER", "less -FRX")
	got := pagerCommand()
	want := []string{"less", "-FRX"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pagerCommand() = %v, want %v", got, want)
	}
}

func TestPagerCommand_FallbackWhenUnset(t *testing.T) {
	t.Setenv("PAGER", "")
	got := pagerCommand()
	want := []string{"less", "-FRX"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pagerCommand() = %v, want %v", got, want)
	}
}
