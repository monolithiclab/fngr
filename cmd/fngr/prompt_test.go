package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "empty defaults to yes", input: "\n", want: true},
		{name: "y", input: "y\n", want: true},
		{name: "yes", input: "yes\n", want: true},
		{name: "uppercase Y", input: "Y\n", want: true},
		{name: "n", input: "n\n", want: false},
		{name: "no", input: "no\n", want: false},
		{name: "garbage", input: "maybe\n", want: false},
		{name: "eof without newline", input: "", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			got, err := confirm(strings.NewReader(tt.input), &out, "Continue? [Y/n] ")
			if err != nil {
				t.Fatalf("confirm: %v", err)
			}
			if got != tt.want {
				t.Errorf("confirm(%q) = %v, want %v", tt.input, got, tt.want)
			}
			if out.String() != "Continue? [Y/n] " {
				t.Errorf("prompt not written; got %q", out.String())
			}
		})
	}
}
