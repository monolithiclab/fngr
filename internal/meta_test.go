package internal

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

func TestParseBodyTags(t *testing.T) {
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
			assertMetaEqual(t, ParseBodyTags(tt.text), tt.want)
		})
	}
}

func TestParseFlagMeta(t *testing.T) {
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
			got, err := ParseFlagMeta(tt.flags)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseFlagMeta(%v) expected error, got nil", tt.flags)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFlagMeta(%v) unexpected error: %v", tt.flags, err)
			}
			assertMetaEqual(t, got, tt.want)
		})
	}
}

func TestCollectMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		flags   []string
		author  string
		want    []Meta
		wantErr bool
	}{
		{
			name:   "combines all sources",
			text:   "Deploy done #ops @sarah",
			flags:  []string{"env=prod"},
			author: "nicolas",
			want: []Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "people", Value: "sarah"},
				{Key: "tag", Value: "ops"},
				{Key: "env", Value: "prod"},
			},
		},
		{
			name:   "deduplicates across sources",
			text:   "#ops",
			flags:  []string{"tag=ops"},
			author: "nicolas",
			want: []Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "tag", Value: "ops"},
			},
		},
		{
			name:    "propagates flag parse error",
			text:    "some text",
			flags:   []string{"noequalssign"},
			author:  "nicolas",
			wantErr: true,
		},
		{
			name:   "author only",
			text:   "plain text",
			flags:  nil,
			author: "nicolas",
			want: []Meta{
				{Key: "author", Value: "nicolas"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := CollectMeta(tt.text, tt.flags, tt.author)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("CollectMeta expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("CollectMeta unexpected error: %v", err)
			}
			assertMetaEqual(t, got, tt.want)
		})
	}
}

func TestBuildFTSContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		text string
		meta []Meta
		want string
	}{
		{
			name: "text with metadata",
			text: "Deploy done",
			meta: []Meta{
				{Key: "author", Value: "nicolas"},
				{Key: "tag", Value: "ops"},
			},
			want: "Deploy done author=nicolas tag=ops",
		},
		{
			name: "text only",
			text: "Just some text",
			meta: nil,
			want: "Just some text",
		},
		{
			name: "empty text with metadata",
			text: "",
			meta: []Meta{
				{Key: "author", Value: "nicolas"},
			},
			want: "author=nicolas",
		},
		{
			name: "empty text and no metadata",
			text: "",
			meta: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := BuildFTSContent(tt.text, tt.meta)
			if got != tt.want {
				t.Errorf("BuildFTSContent(%q, %v) = %q, want %q", tt.text, tt.meta, got, tt.want)
			}
		})
	}
}
