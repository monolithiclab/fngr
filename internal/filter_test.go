package internal

import (
	"testing"
)

func TestTokenizeFilter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty input", "", nil},
		{"bare word", "project", []string{"project"}},
		{"hash tag", "#ops", []string{"#ops"}},
		{"at tag", "@sarah", []string{"@sarah"}},
		{"key=value", "tag=deploy", []string{"tag=deploy"}},
		{"AND operator", "tag=deploy & project", []string{"tag=deploy", "&", "project"}},
		{"OR operator", "#ops | #deploy", []string{"#ops", "|", "#deploy"}},
		{"NOT prefix", "@user & !daily", []string{"@user", "&", "!daily"}},
		{"complex expression", "author=nicolas & #work & !meeting", []string{"author=nicolas", "&", "#work", "&", "!meeting"}},
		{"NOT with key=value", "!tag=deploy", []string{"!tag=deploy"}},
		{"hierarchical tag", "#work/project-x", []string{"#work/project-x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenizeFilter(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("tokenizeFilter(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.want, len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("tokenizeFilter(%q)[%d] = %q, want %q",
						tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestPreprocessFilter(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"hash tag shorthand", "#ops", `"tag=ops"`},
		{"at tag shorthand", "@sarah", `"people=sarah"`},
		{"key=value passthrough", "tag=deploy", `"tag=deploy"`},
		{"bare word passthrough", "project", "project"},
		{"AND operator", "tag=deploy & project", `"tag=deploy" AND project`},
		{"OR operator", "#ops | #deploy", `"tag=ops" OR "tag=deploy"`},
		{"NOT operator", "@user & !daily", `"people=user" AND NOT daily`},
		{"complex expression", "author=nicolas & #work & !meeting", `"author=nicolas" AND "tag=work" AND NOT meeting`},
		{"hierarchical tag", "#work/project-x", `"tag=work/project-x"`},
		{"empty input", "", ""},
		{"NOT with key=value", "!tag=deploy", `NOT "tag=deploy"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PreprocessFilter(tt.input)
			if got != tt.want {
				t.Errorf("PreprocessFilter(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
