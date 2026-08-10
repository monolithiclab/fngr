package main

import (
	"bytes"
	"os/user"
	"strings"
	"testing"
)

func TestExitCode(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		kong int
		want int
	}{
		{"success passes through", 0, 0},
		{"diagnosed error passes through", 1, exitError},
		{"kong's own usage status becomes ours", kongUsageStatus, exitUsage},
		// FatalIfErrorf runs every error through kong.ExitCoder, so a command
		// that wraps an *exec.ExitError ($EDITOR exiting 3) arrives here as 3.
		// That is a run fngr attempted and reported on, not a command line it
		// refused to parse — the contract calls that exitError.
		{"an exit status a command propagated", 3, exitError},
		{"any other status", 42, exitError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := exitCode(tt.kong); got != tt.want {
				t.Errorf("exitCode(%d) = %d, want %d", tt.kong, got, tt.want)
			}
		})
	}
}

// TestKongOptions_ParseErrorIsShortAndExitsTwo pins both halves of the exit
// contract at the layer that decides them, since neither is reachable from the
// dispatch harness — it never calls FatalIfErrorf.
//
// The line count is the assertion that matters as much as the status: with
// kong.UsageOnError the whole command list came first and `unknown flag --bogus`
// landed 25 lines below the fold.
func TestKongOptions_ParseErrorIsShortAndExitsTwo(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	status := -1
	parser := newTestParser(t, &stdout, &stderr, func(code int) { status = code })

	_, parseErr := parser.Parse([]string{"--bogus"})
	if parseErr == nil {
		t.Fatal("parse of --bogus succeeded, want an error")
	}
	parser.FatalIfErrorf(parseErr)

	if status != exitUsage {
		t.Errorf("exit status = %d, want %d — 80 is Kong's number, not fngr's", status, exitUsage)
	}
	if got := strings.Count(stdout.String()+stderr.String(), "\n"); got > 5 {
		t.Errorf("parse error wrote %d lines, want a short usage:\n%s%s", got, stdout.String(), stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "unknown flag --bogus") {
		t.Errorf("stderr = %q, want it to name the flag", got)
	}
}

// TestKongOptions_HelpStillSucceeds guards the other side of the mapping:
// --help is a request that was answered, not a command line that failed, and
// Kong signals it through the same exit function. It is also the case that
// says the mapping cannot be "anything non-zero is a usage error".
//
// Parse's own error is ignored on purpose: Kong runs the help hook and expects
// the exit function not to return, so a capturing stub leaves it parsing an
// argv it has already answered.
func TestKongOptions_HelpStillSucceeds(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	status := -1
	parser := newTestParser(t, &stdout, &stderr, func(code int) { status = code })

	_, _ = parser.Parse([]string{"--help"})

	if status != 0 {
		t.Errorf("exit status = %d, want 0", status)
	}
	if got := stdout.String(); !strings.Contains(got, "Commands:") {
		t.Errorf("--help printed no command list:\n%s", got)
	}
}

func TestCurrentUser_PrefersTheEnvironment(t *testing.T) {
	t.Setenv("USER", "from-env")
	if got := currentUser(); got != "from-env" {
		t.Errorf("currentUser() = %q, want %q", got, "from-env")
	}
}

// TestCurrentUser_FallsBackWhenUSERIsUnset pins the fallback against os/user
// itself rather than against a literal: the account the test runs as is the
// host's to decide, and an empty answer is legitimate in a container with no
// passwd entry. Both branches of the fallback are covered — whichever one this
// host takes, the expectation is derived the same way.
func TestCurrentUser_FallsBackWhenUSERIsUnset(t *testing.T) {
	t.Setenv("USER", "")
	want := ""
	if u, err := user.Current(); err == nil {
		want = u.Username
	}
	if got := currentUser(); got != want {
		t.Errorf("currentUser() = %q, want %q", got, want)
	}
}
