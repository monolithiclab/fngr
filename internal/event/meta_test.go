package event

import (
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

func assertMetaEqual(t *testing.T, got, want []parse.Meta) {
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

func TestCollectMeta(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		text    string
		flags   []string
		author  string
		want    []parse.Meta
		wantErr bool
	}{
		{
			name:   "combines all sources",
			text:   "Deploy done #ops @sarah",
			flags:  []string{"env=prod"},
			author: "nicolas",
			want: []parse.Meta{
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
			want: []parse.Meta{
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
			want: []parse.Meta{
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
