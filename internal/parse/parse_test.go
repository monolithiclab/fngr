package parse

import (
	"testing"
)

func assertMetaEqual(t *testing.T, got, want []Meta) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestBodyTags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		want []Meta
	}{
		{
			name: "hash tags",
			text: "Meeting about #planning and #budget",
			want: []Meta{
				{Key: "tag", Value: "planning"},
				{Key: "tag", Value: "budget"},
			},
		},
		{
			name: "at tags",
			text: "Talked with @sarah and @bob",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "people", Value: "bob"},
			},
		},
		{
			name: "mixed tags with people first",
			text: "Discussed #planning with @sarah",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "planning"},
			},
		},
		{
			name: "hierarchical tags",
			text: "Working on #work/project-x and #infra/deploy-v2",
			want: []Meta{
				{Key: "tag", Value: "work/project-x"},
				{Key: "tag", Value: "infra/deploy-v2"},
			},
		},
		{
			name: "no tags",
			text: "Just a plain text entry",
			want: nil,
		},
		{
			name: "empty text",
			text: "",
			want: nil,
		},
		{
			name: "duplicate tags are deduplicated",
			text: "#planning and #planning again #planning",
			want: []Meta{
				{Key: "tag", Value: "planning"},
			},
		},
		{
			name: "duplicate at tags are deduplicated",
			text: "@sarah and @sarah again",
			want: []Meta{
				{Key: "people", Value: "sarah"},
			},
		},
		{
			name: "mixed duplicates across types",
			text: "@sarah #ops @sarah #ops",
			want: []Meta{
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "ops"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertMetaEqual(t, BodyTags(tt.text), tt.want)
		})
	}
}

func TestKeyValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     string
		wantKey   string
		wantValue string
		wantErr   bool
	}{
		{name: "ok", input: "env=prod", wantKey: "env", wantValue: "prod"},
		{name: "value contains =", input: "note=a=b", wantKey: "note", wantValue: "a=b"},
		{name: "empty value", input: "tag=", wantKey: "tag", wantValue: ""},
		{name: "missing =", input: "noeq", wantErr: true},
		{name: "empty input", input: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			k, v, err := KeyValue(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("KeyValue(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("KeyValue(%q) unexpected error: %v", tt.input, err)
			}
			if k != tt.wantKey || v != tt.wantValue {
				t.Errorf("KeyValue(%q) = (%q, %q), want (%q, %q)", tt.input, k, v, tt.wantKey, tt.wantValue)
			}
		})
	}
}

func TestFlagMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		flags   []string
		want    []Meta
		wantErr bool
	}{
		{
			name:  "single flag",
			flags: []string{"env=prod"},
			want:  []Meta{{Key: "env", Value: "prod"}},
		},
		{
			name:  "multiple flags",
			flags: []string{"env=prod", "region=us-east-1"},
			want: []Meta{
				{Key: "env", Value: "prod"},
				{Key: "region", Value: "us-east-1"},
			},
		},
		{
			name:    "missing equals sign",
			flags:   []string{"invalidflag"},
			wantErr: true,
		},
		{
			name:  "value with equals signs",
			flags: []string{"note=a=b=c"},
			want:  []Meta{{Key: "note", Value: "a=b=c"}},
		},
		{
			name:  "empty flags",
			flags: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FlagMeta(tt.flags)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("FlagMeta(%v) expected error, got nil", tt.flags)
				}
				return
			}
			if err != nil {
				t.Fatalf("FlagMeta(%v) unexpected error: %v", tt.flags, err)
			}
			assertMetaEqual(t, got, tt.want)
		})
	}
}

func TestFTSContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		title string
		body  string
		meta  []Meta
		want  string
	}{
		{"empty", "", "", nil, ""},
		{"title only", "hello", "", nil, "hello"},
		{"body only", "", "world", nil, "world"},
		{"title + body", "hello", "world", nil, "hello world"},
		{
			"title + body + meta",
			"hello", "world",
			[]Meta{{Key: "tag", Value: "ops"}, {Key: "people", Value: "sarah"}},
			"hello world tag=ops people=sarah",
		},
		{
			"empty title with meta",
			"", "world",
			[]Meta{{Key: "tag", Value: "ops"}},
			"world tag=ops",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FTSContent(tt.title, tt.body, tt.meta); got != tt.want {
				t.Errorf("FTSContent(%q, %q, %v) = %q, want %q",
					tt.title, tt.body, tt.meta, got, tt.want)
			}
		})
	}
}

func TestMetaArg(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{name: "people", input: "@Sarah", wantKey: "people", wantVal: "Sarah"},
		{name: "tag", input: "#ops", wantKey: "tag", wantVal: "ops"},
		{name: "key=value", input: "env=prod", wantKey: "env", wantVal: "prod"},
		{name: "value with =", input: "note=a=b", wantKey: "note", wantVal: "a=b"},
		{name: "hierarchical tag", input: "#work/project-x", wantKey: "tag", wantVal: "work/project-x"},
		{name: "empty value", input: "k=", wantKey: "k", wantVal: ""},

		{name: "bare word", input: "urgent", wantErr: true},
		{name: "lone @", input: "@", wantErr: true},
		{name: "lone #", input: "#", wantErr: true},
		{name: "@ with space", input: "@ Sarah", wantErr: true},
		{name: "missing key", input: "=value", wantErr: true},
		{name: "empty input", input: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MetaArg(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("MetaArg(%q) expected error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("MetaArg(%q) err = %v", tt.input, err)
			}
			if got.Key != tt.wantKey || got.Value != tt.wantVal {
				t.Errorf("MetaArg(%q) = (%q, %q), want (%q, %q)",
					tt.input, got.Key, got.Value, tt.wantKey, tt.wantVal)
			}
		})
	}
}

func TestSplitTitleBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		title string
		body  string
	}{
		{"", "", ""},
		{"   ", "", ""},
		{"hello", "hello", ""},
		{"hello.", "hello.", ""},
		{"hello. world", "hello", "world"},
		{"  hello  .  world  ", "hello", "world"},
		{"v1.2 done. Hotfix", "v1.2 done", "Hotfix"},
		{"v1.2.3 released", "v1.2.3 released", ""},
		{"first. second. third", "first", "second. third"},
		{". hello", "", "hello"},
		{"hello. ", "hello", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			gotTitle, gotBody := SplitTitleBody(tt.input)
			if gotTitle != tt.title || gotBody != tt.body {
				t.Errorf("SplitTitleBody(%q) = (%q, %q), want (%q, %q)",
					tt.input, gotTitle, gotBody, tt.title, tt.body)
			}
		})
	}
}
