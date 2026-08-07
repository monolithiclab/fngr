package main

import (
	"slices"
	"testing"
)

// TestEnvCommand covers the shared tokenizer directly, including the two
// shapes only one caller exercises: a variable set to whitespace (which must
// read as unset, so $PAGER=" " still falls back to less) and the first-set-
// wins precedence $VISUAL/$EDITOR relies on.
func TestEnvCommand(t *testing.T) {
	tests := []struct {
		name  string
		set   map[string]string
		names []string
		want  []string
	}{
		{name: "unset", names: []string{"FNGR_T1"}},
		{name: "empty", set: map[string]string{"FNGR_T1": ""}, names: []string{"FNGR_T1"}},
		{name: "whitespace only", set: map[string]string{"FNGR_T1": "  \t "}, names: []string{"FNGR_T1"}},
		{name: "single word", set: map[string]string{"FNGR_T1": "less"}, names: []string{"FNGR_T1"}, want: []string{"less"}},
		{
			name: "flags and surrounding space", set: map[string]string{"FNGR_T1": "  code -w  --wait "},
			names: []string{"FNGR_T1"}, want: []string{"code", "-w", "--wait"},
		},
		{
			name: "first set wins", set: map[string]string{"FNGR_T1": "vi", "FNGR_T2": "nano"},
			names: []string{"FNGR_T1", "FNGR_T2"}, want: []string{"vi"},
		},
		{
			name: "falls through an empty one", set: map[string]string{"FNGR_T1": " ", "FNGR_T2": "nano"},
			names: []string{"FNGR_T1", "FNGR_T2"}, want: []string{"nano"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: t.Setenv is process-wide.
			t.Setenv("FNGR_T1", "")
			t.Setenv("FNGR_T2", "")
			for k, v := range tt.set {
				t.Setenv(k, v)
			}
			if got := envCommand(tt.names...); !slices.Equal(got, tt.want) {
				t.Errorf("envCommand = %q, want %q", got, tt.want)
			}
		})
	}
}
