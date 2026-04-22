package main

import (
	"context"
	"io"
	"iter"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

// eventStore is the narrow surface that CLI commands depend on. *event.Store
// satisfies it for production; tests provide their own implementations.
type eventStore interface {
	Add(ctx context.Context, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error)
	AddMany(ctx context.Context, inputs []event.AddInput) ([]int64, error)
	AddTags(ctx context.Context, id int64, tags []parse.Meta) (int64, error)
	Get(ctx context.Context, id int64) (*event.Event, error)
	Delete(ctx context.Context, id int64) error
	Update(ctx context.Context, id int64, text *string, createdAt *time.Time) error
	Reparent(ctx context.Context, id int64, newParent *int64) error
	RemoveTags(ctx context.Context, id int64, tags []parse.Meta) (int64, error)
	HasChildren(ctx context.Context, id int64) (bool, error)
	List(ctx context.Context, opts event.ListOpts) ([]event.Event, error)
	ListSeq(ctx context.Context, opts event.ListOpts) iter.Seq2[event.Event, error]
	GetSubtree(ctx context.Context, rootID int64) ([]event.Event, error)
	ListMeta(ctx context.Context, opts event.ListMetaOpts) ([]event.MetaCount, error)
	CountMeta(ctx context.Context, key, value string) (int64, error)
	UpdateMeta(ctx context.Context, oldKey, oldValue, newKey, newValue string) (int64, error)
	DeleteMeta(ctx context.Context, key, value string) (int64, error)
}

// ioStreams bundles the input and output streams used by command handlers,
// kept injectable so commands can be exercised in tests without touching os.
type ioStreams struct {
	In    io.Reader
	Out   io.Writer
	Err   io.Writer // editor cancel notice; stays out of stdout for script piping
	IsTTY bool      // true when stdin is an interactive terminal
}
