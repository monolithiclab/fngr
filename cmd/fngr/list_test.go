package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/parse"
)

func TestListCmd_DefaultTree(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "deploy #ops", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "ops"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "tree"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "deploy #ops") {
		t.Errorf("output = %q, want contains event text", got)
	}
}

func TestListCmd_JSON(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "json me", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("json output invalid: %v\n%s", err, out.String())
	}
	if len(parsed) != 1 || parsed[0]["text"] != "json me" {
		t.Errorf("parsed = %v", parsed)
	}
}

func TestListCmd_InvalidFromDate(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, _ := newTestIO("")

	cmd := &ListCmd{From: "not-a-date"}
	err := cmd.Run(s, io)
	if err == nil || !strings.Contains(err.Error(), "--from") {
		t.Errorf("error = %v, want --from parse error", err)
	}
}

func TestListCmd_FilterAndDateRange(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "match", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "ops"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := s.Add(context.Background(), "skip", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
		{Key: "tag", Value: "work"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "flat", Filter: "#ops"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "match") || strings.Contains(got, "skip") {
		t.Errorf("output = %q, want only 'match'", got)
	}
}
