package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/monolithiclab/fngr/internal/db"
	"github.com/monolithiclab/fngr/internal/event"
)

func newTestStore(t *testing.T) *event.Store {
	t.Helper()
	database, err := db.Open(":memory:", true)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return event.NewStore(database)
}

func newTestIO(stdin string) (ioStreams, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return ioStreams{In: strings.NewReader(stdin), Out: out}, out
}
