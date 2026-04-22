package event

import (
	"context"
	"database/sql"
	"iter"
	"time"

	"github.com/monolithiclab/fngr/internal/parse"
)

// Store is a thin wrapper around *sql.DB that exposes the package's data
// access functions as methods. CLI commands depend on narrow consumer-side
// interfaces; *Store satisfies them all.
type Store struct {
	DB *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{DB: db} }

func (s *Store) Add(ctx context.Context, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error) {
	return Add(ctx, s.DB, text, parentID, meta, createdAt)
}

func (s *Store) Get(ctx context.Context, id int64) (*Event, error) {
	return Get(ctx, s.DB, id)
}

func (s *Store) Delete(ctx context.Context, id int64) error {
	return Delete(ctx, s.DB, id)
}

func (s *Store) Update(ctx context.Context, id int64, text *string, createdAt *time.Time) error {
	return Update(ctx, s.DB, id, text, createdAt)
}

func (s *Store) HasChildren(ctx context.Context, id int64) (bool, error) {
	return HasChildren(ctx, s.DB, id)
}

func (s *Store) List(ctx context.Context, opts ListOpts) ([]Event, error) {
	return List(ctx, s.DB, opts)
}

func (s *Store) ListSeq(ctx context.Context, opts ListOpts) iter.Seq2[Event, error] {
	return ListSeq(ctx, s.DB, opts)
}

func (s *Store) GetSubtree(ctx context.Context, rootID int64) ([]Event, error) {
	return GetSubtree(ctx, s.DB, rootID)
}

func (s *Store) ListMeta(ctx context.Context) ([]MetaCount, error) {
	return ListMeta(ctx, s.DB)
}

func (s *Store) CountMeta(ctx context.Context, key, value string) (int64, error) {
	return CountMeta(ctx, s.DB, key, value)
}

func (s *Store) UpdateMeta(ctx context.Context, oldKey, oldValue, newKey, newValue string) (int64, error) {
	return UpdateMeta(ctx, s.DB, oldKey, oldValue, newKey, newValue)
}

func (s *Store) DeleteMeta(ctx context.Context, key, value string) (int64, error) {
	return DeleteMeta(ctx, s.DB, key, value)
}

func (s *Store) Reparent(ctx context.Context, id int64, newParent *int64) error {
	return Reparent(ctx, s.DB, id, newParent)
}

func (s *Store) AddTags(ctx context.Context, id int64, tags []parse.Meta) error {
	return AddTags(ctx, s.DB, id, tags)
}

func (s *Store) RemoveTags(ctx context.Context, id int64, tags []parse.Meta) (int64, error) {
	return RemoveTags(ctx, s.DB, id, tags)
}
