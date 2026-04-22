package main

import (
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

func TestParseJSONAddInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		wantLen int
		wantErr string
	}{
		{name: "single-object", input: `{"text":"hi"}`, wantLen: 1},
		{name: "array-of-one", input: `[{"text":"hi"}]`, wantLen: 1},
		{name: "array-of-three", input: `[{"text":"a"},{"text":"b"},{"text":"c"}]`, wantLen: 3},
		{name: "empty-array", input: `[]`, wantLen: 0},
		{name: "malformed-json", input: `{"text":`, wantErr: "--format=json"},
		{name: "scalar-string", input: `"hello"`, wantErr: "--format=json"},
		{name: "scalar-number", input: `42`, wantErr: "--format=json"},
		{name: "with-meta", input: `{"text":"hi","meta":[["tag","ops"]]}`, wantLen: 1},
		{name: "with-parent-and-time", input: `{"text":"hi","parent_id":3,"created_at":"2026-04-01T12:00:00Z"}`, wantLen: 1},
		{name: "unknown-field-single", input: `{"text":"hi","txet":"typo"}`, wantErr: "unknown field"},
		{name: "unknown-field-array", input: `[{"text":"hi","extra":1}]`, wantErr: "unknown field"},
		{name: "leading-whitespace-array", input: "  \n[{\"text\":\"hi\"}]", wantLen: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseJSONAddInput(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJSONAddInput: %v", err)
			}
			if len(got) != tc.wantLen {
				t.Errorf("got %d records, want %d", len(got), tc.wantLen)
			}
		})
	}
}

func TestJSONInputToAddInput(t *testing.T) {
	t.Parallel()

	mkPtr := func(s string) *string { return &s }
	mkInt64 := func(n int64) *int64 { return &n }

	cases := []struct {
		name       string
		in         jsonAddInput
		defaults   cliDefaults
		author     string
		wantText   string
		wantErr    string
		wantAuthor string // when set, asserts the merged meta has exactly this author value
	}{
		{
			name:     "happy-single",
			in:       jsonAddInput{Text: "hi"},
			author:   "alice",
			wantText: "hi",
		},
		{
			name:    "missing-text",
			in:      jsonAddInput{},
			author:  "alice",
			wantErr: "text is required",
		},
		{
			name:    "whitespace-only-text",
			in:      jsonAddInput{Text: "   "},
			author:  "alice",
			wantErr: "text is required",
		},
		{
			name:     "json-meta-overrides-cli",
			in:       jsonAddInput{Text: "x", Meta: [][2]string{{"env", "prod"}}},
			defaults: cliDefaults{meta: []parse.Meta{{Key: "env", Value: "dev"}}},
			author:   "alice",
			wantText: "x",
		},
		{
			name:    "empty-meta-key",
			in:      jsonAddInput{Text: "x", Meta: [][2]string{{"", "v"}}},
			author:  "alice",
			wantErr: "meta[0]: empty key",
		},
		{
			name:    "bad-created-at",
			in:      jsonAddInput{Text: "x", CreatedAt: mkPtr("not-a-time")},
			author:  "alice",
			wantErr: "created_at",
		},
		{
			name:     "json-parent-id-overrides-cli",
			in:       jsonAddInput{Text: "x", ParentID: mkInt64(7)},
			defaults: cliDefaults{parent: mkInt64(3)},
			author:   "alice",
			wantText: "x",
		},
		{
			name:     "valid-created-at",
			in:       jsonAddInput{Text: "x", CreatedAt: mkPtr("2026-04-01T12:00:00Z")},
			author:   "alice",
			wantText: "x",
		},
		{
			name:       "json-author-suppresses-default",
			in:         jsonAddInput{Text: "x", Meta: [][2]string{{"author", "bob"}}},
			author:     "alice",
			wantText:   "x",
			wantAuthor: "bob",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := jsonInputToAddInput(tc.in, tc.defaults, tc.author, 0)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("jsonInputToAddInput: %v", err)
			}
			if got.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tc.wantText)
			}
			if tc.wantAuthor != "" {
				var authors []string
				for _, m := range got.Meta {
					if m.Key == "author" {
						authors = append(authors, m.Value)
					}
				}
				if len(authors) != 1 || authors[0] != tc.wantAuthor {
					t.Errorf("authors = %v, want exactly [%q]", authors, tc.wantAuthor)
				}
			}
		})
	}
}

func TestParseJSONAddInput_BatchSizeLimit(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i <= maxJSONBatchSize; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"text":"x"}`)
	}
	b.WriteByte(']')
	_, err := parseJSONAddInput(b.String())
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("err = %v, want 'exceeds limit'", err)
	}
}
