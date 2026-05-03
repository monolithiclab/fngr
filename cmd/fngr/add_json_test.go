package main

import (
	"context"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/event"
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
		{name: "single-object", input: `{"title":"hi"}`, wantLen: 1},
		{name: "array-of-one", input: `[{"title":"hi"}]`, wantLen: 1},
		{name: "array-of-three", input: `[{"title":"a"},{"title":"b"},{"title":"c"}]`, wantLen: 3},
		{name: "empty-array", input: `[]`, wantLen: 0},
		{name: "malformed-json", input: `{"title":`, wantErr: "--format=json"},
		{name: "scalar-string", input: `"hello"`, wantErr: "--format=json"},
		{name: "scalar-number", input: `42`, wantErr: "--format=json"},
		{name: "with-meta", input: `{"title":"hi","meta":[["tag","ops"]]}`, wantLen: 1},
		{name: "with-parent-and-time", input: `{"title":"hi","parent_id":3,"created_at":"2026-04-01T12:00:00Z"}`, wantLen: 1},
		{name: "unknown-field-single", input: `{"title":"hi","ttile":"typo"}`, wantErr: "unknown field"},
		{name: "unknown-field-array", input: `[{"title":"hi","extra":1}]`, wantErr: "unknown field"},
		{name: "leading-whitespace-array", input: "  \n[{\"title\":\"hi\"}]", wantLen: 1},
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
		wantTitle  string
		wantErr    string
		wantAuthor string // when set, asserts the merged meta has exactly this author value
	}{
		{
			name:      "happy-single",
			in:        jsonAddInput{Title: "hi"},
			author:    "alice",
			wantTitle: "hi",
		},
		{
			name:    "missing-title",
			in:      jsonAddInput{},
			author:  "alice",
			wantErr: "title is required",
		},
		{
			name:    "whitespace-only-title",
			in:      jsonAddInput{Title: "   "},
			author:  "alice",
			wantErr: "title is required",
		},
		{
			name:      "json-meta-overrides-cli",
			in:        jsonAddInput{Title: "x", Meta: [][2]string{{"env", "prod"}}},
			defaults:  cliDefaults{meta: []parse.Meta{{Key: "env", Value: "dev"}}},
			author:    "alice",
			wantTitle: "x",
		},
		{
			name:    "empty-meta-key",
			in:      jsonAddInput{Title: "x", Meta: [][2]string{{"", "v"}}},
			author:  "alice",
			wantErr: "meta[0]: empty key",
		},
		{
			name:    "bad-created-at",
			in:      jsonAddInput{Title: "x", CreatedAt: mkPtr("not-a-time")},
			author:  "alice",
			wantErr: "created_at",
		},
		{
			name:      "json-parent-id-overrides-cli",
			in:        jsonAddInput{Title: "x", ParentID: mkInt64(7)},
			defaults:  cliDefaults{parent: mkInt64(3)},
			author:    "alice",
			wantTitle: "x",
		},
		{
			name:      "valid-created-at",
			in:        jsonAddInput{Title: "x", CreatedAt: mkPtr("2026-04-01T12:00:00Z")},
			author:    "alice",
			wantTitle: "x",
		},
		{
			name:       "json-author-suppresses-default",
			in:         jsonAddInput{Title: "x", Meta: [][2]string{{"author", "bob"}}},
			author:     "alice",
			wantTitle:  "x",
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
			if got.Title != tc.wantTitle {
				t.Errorf("Title = %q, want %q", got.Title, tc.wantTitle)
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
		b.WriteString(`{"title":"x"}`)
	}
	b.WriteByte(']')
	_, err := parseJSONAddInput(b.String())
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Errorf("err = %v, want 'exceeds limit'", err)
	}
}

func TestAddJSON_TitleBody(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO(`{"title":"deploy","body":"hotfix #ops"}`)

	cmd := &AddCmd{Args: []string{`{"title":"deploy","body":"hotfix #ops"}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := s.List(context.Background(), event.ListOpts{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if events[0].Title != "deploy" || events[0].Body != "hotfix #ops" {
		t.Errorf("got (title=%q, body=%q), want (deploy, hotfix #ops)", events[0].Title, events[0].Body)
	}
}

func TestAddJSON_BodyOptional(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &AddCmd{Args: []string{`{"title":"hello"}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, _ := s.List(context.Background(), event.ListOpts{})
	if events[0].Title != "hello" || events[0].Body != "" {
		t.Errorf("got (title=%q, body=%q), want (hello, '')", events[0].Title, events[0].Body)
	}
}

func TestAddJSON_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	cmd := &AddCmd{Args: []string{`{"title":""}`}, Author: "alice", Format: "json"}
	if err := cmd.Run(s, io); err == nil {
		t.Error("expected empty-title error")
	}
}

func TestAddJSON_RejectsTextField(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")
	cmd := &AddCmd{Args: []string{`{"text":"old"}`}, Author: "alice", Format: "json"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("expected unknown-field error on `text`, got %v", err)
	}
}
