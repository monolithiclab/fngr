package event

import (
	"strings"
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

// TestAuthorOf covers the read side of the single-author rule, including the
// arbitrary answer it gives when the rule is broken — that answer is exactly
// why the write paths refuse to break it.
func TestAuthorOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		meta []parse.Meta
		want string
	}{
		{name: "nil", want: ""},
		{
			name: "no author key",
			meta: []parse.Meta{{Key: MetaKeyTag, Value: "ops"}},
			want: "",
		},
		{
			name: "author among others",
			meta: []parse.Meta{{Key: MetaKeyTag, Value: "ops"}, {Key: MetaKeyAuthor, Value: "nicolas"}},
			want: "nicolas",
		},
		{
			name: "two authors: the first wins, whichever that is",
			meta: []parse.Meta{{Key: MetaKeyAuthor, Value: "bob"}, {Key: MetaKeyAuthor, Value: "nicolas"}},
			want: "bob",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := AuthorOf(tt.meta); got != tt.want {
				t.Errorf("AuthorOf(%v) = %q, want %q", tt.meta, got, tt.want)
			}
		})
	}
}

// TestMergeMeta_Author is the M3 guard at the creation end. `author` is
// looked up by name at render time and the lookup returns whichever tuple
// sorts first, so two author rows on one event mean the displayed author is
// alphabetical rather than true. MergeMeta is the only place that can prevent
// the second row from ever being written.
func TestMergeMeta_Author(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		text          string
		explicit      []parse.Meta
		defaultAuthor string
		want          []parse.Meta
		wantErr       string
	}{
		{
			name:          "explicit author replaces the default",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "sarah"}},
			defaultAuthor: "nicolas",
			want:          []parse.Meta{{Key: "author", Value: "sarah"}},
		},
		{
			name:          "explicit author is ordered first",
			text:          "ship it #ops",
			explicit:      []parse.Meta{{Key: "env", Value: "prod"}, {Key: "author", Value: "sarah"}},
			defaultAuthor: "nicolas",
			want: []parse.Meta{
				{Key: "author", Value: "sarah"},
				{Key: "tag", Value: "ops"},
				{Key: "env", Value: "prod"},
			},
		},
		{
			name:          "explicit author repeating the default is not a conflict",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "nicolas"}},
			defaultAuthor: "nicolas",
			want:          []parse.Meta{{Key: "author", Value: "nicolas"}},
		},
		{
			name:          "two distinct explicit authors conflict",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "sarah"}, {Key: "author", Value: "bob"}},
			defaultAuthor: "nicolas",
			wantErr:       "exactly one author",
		},
		{
			// The pair that slipped past a first draft comparing against
			// defaultAuthor instead of tracking "an explicit one was seen":
			// the first entry leaves author == defaultAuthor, so the second
			// looked like the only explicit one.
			name:          "two explicit authors, the first matching the default",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: "nicolas"}, {Key: "author", Value: "bob"}},
			defaultAuthor: "nicolas",
			wantErr:       "exactly one author",
		},
		{
			name:          "no author at all when the default is empty",
			text:          "ship it #ops",
			defaultAuthor: "",
			want:          []parse.Meta{{Key: "tag", Value: "ops"}},
		},
		{
			// `-m author=` used to store a blank author *and* discard the
			// real one: the empty value suppressed the synthesised tuple and
			// then got appended by the explicit pass. No meta verb can repair
			// author, so the event was stuck that way.
			name:          "empty explicit author is rejected, not ignored",
			text:          "plain",
			explicit:      []parse.Meta{{Key: "author", Value: ""}},
			defaultAuthor: "nicolas",
			wantErr:       "author cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := MergeMeta(tt.text, tt.explicit, tt.defaultAuthor)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("MergeMeta err = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MergeMeta: %v", err)
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
