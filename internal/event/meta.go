package event

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/monolithiclab/fngr/internal/parse"
)

// Well-known meta keys. The matching value's domain meaning is encoded
// in the key, so renderers and CLI verbs can recognise them by name.
const (
	// MetaKeyAuthor identifies the user who recorded the event. Auto-set
	// from --author / $FNGR_AUTHOR / $USER; single-valued and immutable
	// once the event exists (see protectedMetaKeys).
	MetaKeyAuthor = "author"
	// MetaKeyPeople holds names extracted from `@person` body shorthand.
	MetaKeyPeople = "people"
	// MetaKeyTag holds names extracted from `#tag` body shorthand.
	MetaKeyTag = "tag"
)

// event_meta.source values (migration 5). Provenance exists for exactly one
// decision: an edit that drops an inline `@bob` retracts the tag it minted,
// and must leave an identical tuple the operator asked for standing. Without
// it the two are the same row and the sync took both.
const (
	// metaSourceBody marks a tuple derived from `@person` / `#tag` in the
	// event's own title or body. Only these are removed by Update's sync.
	metaSourceBody = "body"
	// metaSourceExplicit marks a tuple the operator supplied — `--meta`,
	// `event tag`, `meta rename`. Also the column default, so any row an
	// older build wrote reads as explicit until migration 5 classifies it.
	metaSourceExplicit = "explicit"
)

// MergeMeta merges the meta sources for a new event into one deduped slice
// in a deterministic order: the author tuple, then body-derived `@person` /
// `#tag` tags, then the remaining explicit entries.
//
// An explicit `author` replaces defaultAuthor rather than joining it —
// author is single-valued, and appending both left two `author` rows on the
// event with the alphabetically-first one silently winning every render.
// For the same reason two explicit authors are an error, not a merge.
// defaultAuthor may be empty, in which case no author tuple is synthesised
// and the caller is responsible for deciding whether that is acceptable.
func MergeMeta(text string, explicit []parse.Meta, defaultAuthor string) ([]parse.Meta, error) {
	author := defaultAuthor
	explicitAuthor := false
	for _, m := range explicit {
		if m.Key != MetaKeyAuthor {
			continue
		}
		// An empty explicit author is rejected rather than ignored. Falling
		// back to defaultAuthor would silently do something other than what
		// was asked; keeping it would store a blank author that no verb can
		// repair, since author is protected against every meta mutation.
		if m.Value == "" {
			return nil, fmt.Errorf("%s cannot be empty", MetaKeyAuthor)
		}
		// Tracked with its own flag rather than by comparing against
		// defaultAuthor: `-m author=x -m author=y` must be rejected even
		// when x happens to equal the default.
		if explicitAuthor && m.Value != author {
			return nil, fmt.Errorf("conflicting %s values %q and %q: an event has exactly one author", MetaKeyAuthor, author, m.Value)
		}
		author, explicitAuthor = m.Value, true
	}

	bodyTags := parse.BodyTags(text)
	seen := make(map[parse.Meta]struct{})
	result := make([]parse.Meta, 0, 1+len(bodyTags)+len(explicit))
	add := func(m parse.Meta) {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			result = append(result, m)
		}
	}

	// Author first even when it came from `explicit`, so the tuple order is
	// the same however the author was supplied.
	if author != "" {
		add(parse.Meta{Key: MetaKeyAuthor, Value: author})
	}
	for _, m := range bodyTags {
		add(m)
	}
	for _, m := range explicit {
		add(m)
	}
	return result, nil
}

// AuthorOf returns the author recorded in meta, or "" if there is none. It is
// the single reading of a rule the write paths guarantee — at most one author
// tuple per event — so `render` and the CLI cannot drift from each other on
// what happens when that guarantee is somehow broken.
func AuthorOf(meta []parse.Meta) string {
	for _, m := range meta {
		if m.Key == MetaKeyAuthor {
			return m.Value
		}
	}
	return ""
}

// requireOneAuthor rejects a merged meta slice carrying two different authors
// or a blank one. MergeMeta already guarantees both for the two CLI paths;
// this is the same invariant restated at the writer, so a directly-built
// AddInput cannot create the row that AuthorOf reads arbitrarily and no meta
// verb is allowed to repair. Zero authors stays legal — the caller decides
// whether an unattributed event is acceptable.
func requireOneAuthor(meta []parse.Meta) error {
	author, found := "", false
	for _, m := range meta {
		if m.Key != MetaKeyAuthor {
			continue
		}
		if m.Value == "" {
			return fmt.Errorf("%s cannot be empty", MetaKeyAuthor)
		}
		if found && m.Value != author {
			return fmt.Errorf("conflicting %s values %q and %q: an event has exactly one author", MetaKeyAuthor, author, m.Value)
		}
		author, found = m.Value, true
	}
	return nil
}

// requireStorableMeta applies parse.ValidateMeta — which states the rule and
// the reasoning — to every tuple about to be written. It is at the writer for
// the reason requireOneAuthor is: parse.Meta is an exported struct with
// exported fields, so a directly-built AddInput bypasses every argument
// parser, and an empty-valued row is unrepairable once written, since the
// verbs that would name it have to spell out the value they are removing.
//
// The CLI checks anyway, but as a pre-flight rather than a second guarantee:
// `--meta` can name the flag and the JSON import the record and pair index,
// neither of which reaches here.
//
// Safe against the paths that see no argument parser at all: parse.BodyTags
// is bounded by metaNamePattern, so neither a body-derived tuple nor a
// migration back-fill re-deriving one can carry an empty value or a key that
// is not a single term.
func requireStorableMeta(meta []parse.Meta) error {
	for _, m := range meta {
		if err := parse.ValidateMeta(m.Key, m.Value); err != nil {
			return err
		}
	}
	return nil
}

// protectedMetaKeys are single-valued and fixed at insert time. Every meta
// verb refuses them as a *target* — adding a second one, removing the only
// one, or renaming another key onto them all leave the event in a state no
// insert path can produce, and renderers that look the key up by name
// (event.AuthorOf) then pick whichever tuple sorts first or print a blank
// column. `Add` is the sole writer, and requireOneAuthor is what keeps it to
// one — the data layer refuses to repair only an invariant it also enforces.
var protectedMetaKeys = map[string]bool{
	MetaKeyAuthor: true,
}

// requireUnprotectedMeta rejects a protected key as the target of verb.
func requireUnprotectedMeta(verb, key string) error {
	if protectedMetaKeys[key] {
		return fmt.Errorf("cannot %s meta key %q: it is single-valued and set when the event is created", verb, key)
	}
	return nil
}

// requireUnprotectedTags applies requireUnprotectedMeta across a tag slice.
func requireUnprotectedTags(verb string, tags []parse.Meta) error {
	for _, m := range tags {
		if err := requireUnprotectedMeta(verb, m.Key); err != nil {
			return err
		}
	}
	return nil
}

// requireRenamableMeta is the protected-key gate for UpdateMeta, which is
// looser than the other verbs' by one case. What protection buys is the count:
// exactly one `author` row per event. A rename that changes the key breaks it
// in one direction or the other — `author=x` → `k=v` strips the author off
// every event that had it, `k=v` → `author=evil` mints a second one — and is
// refused at both ends. A rename that keeps the key only rewrites the value,
// leaving every event with the single row it already had, so a mistyped
// `--author` stays correctable: refusing it too made the one field no verb can
// touch also the one field no verb can fix, and the workaround was re-adding
// the event under a new id. An empty new value is still refused — for every
// key, not just a protected one, since that blank is exactly the unrepairable
// state — via parse.ValidateMeta, which is also what keeps a rename from
// minting the whitespace key `-m` refuses.
func requireRenamableMeta(oldKey, newKey, newValue string) error {
	// The new tuple is minted, so it has to be one a search can reach —
	// including the empty value the protected-key branch below used to be the
	// only guard against. The *old* one is deliberately unchecked: a rename is
	// how an unfindable row an older build wrote gets repaired.
	if err := parse.ValidateMeta(newKey, newValue); err != nil {
		return err
	}
	if oldKey == newKey {
		return nil
	}
	if err := requireUnprotectedMeta("rename", oldKey); err != nil {
		return err
	}
	// The target matters as much as the source: without this, `meta rename
	// k=v author=evil` minted a second author on every event carrying k=v.
	return requireUnprotectedMeta("rename onto", newKey)
}

// CollectMeta is MergeMeta over `--meta key=value` flag strings. Errors out
// if any flag fails to parse as `key=value`.
func CollectMeta(text string, flags []string, author string) ([]parse.Meta, error) {
	explicit, err := parse.FlagMeta(flags)
	if err != nil {
		return nil, err
	}
	return MergeMeta(text, explicit, author)
}

// metaSet indexes tuples for membership tests. parse.Meta is comparable, so
// the tuple is its own key.
func metaSet(tuples []parse.Meta) map[parse.Meta]struct{} {
	set := make(map[parse.Meta]struct{}, len(tuples))
	for _, m := range tuples {
		set[m] = struct{}{}
	}
	return set
}

// subtractMeta returns the tuples in a that are absent from b, preserving
// a's order. Used by Update to narrow the body-tag delete set to tags the
// edit actually removed.
func subtractMeta(a, b []parse.Meta) []parse.Meta {
	if len(a) == 0 || len(b) == 0 {
		return a
	}
	keep := metaSet(b)
	result := make([]parse.Meta, 0, len(a))
	for _, m := range a {
		if _, ok := keep[m]; !ok {
			result = append(result, m)
		}
	}
	return result
}

// AddTags inserts the given meta entries for event id as explicit metadata:
// a tag named on the command line is the operator's, and a later body edit
// that happens to drop the same `@name` must not retract it, so a row already
// present as body-derived is promoted rather than left alone. Returns the
// number of rows that did not already exist, so callers can report "M added,
// K already present" — counted from a pre-read rather than RowsAffected,
// which cannot tell an insert from that promotion. FTS is rebuilt in the same
// transaction. Returns ErrNotFound if the event is missing. Empty `tags` is a
// no-op returning (0, nil). Protected keys (`author`) are refused outright —
// see protectedMetaKeys.
func AddTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) (int64, error) {
	if len(tags) == 0 {
		return 0, nil
	}
	if err := requireUnprotectedTags("add", tags); err != nil {
		return 0, err
	}
	// AddTags mints; RemoveTags below deliberately does not check, so a row an
	// older build wrote can still be named and taken out.
	if err := requireStorableMeta(tags); err != nil {
		return 0, err
	}
	return inTx(ctx, db, func(tx *sql.Tx) (int64, error) {
		return addTagsInTx(ctx, tx, id, tags)
	})
}

// addTagsInTx is AddTags' body.
func addTagsInTx(ctx context.Context, tx *sql.Tx, id int64, tags []parse.Meta) (int64, error) {
	if err := requireEventExists(ctx, tx, id); err != nil {
		return 0, err
	}

	existing, err := readMetaTx(ctx, tx, id)
	if err != nil {
		return 0, err
	}
	present := metaSet(existing)

	stmt, err := tx.PrepareContext(ctx,
		// The DO UPDATE is guarded so it fires only for the promotion it
		// exists for; an already-explicit row is left untouched rather than
		// rewritten to the value it already holds.
		"INSERT INTO event_meta (event_id, key, value, source) VALUES (?, ?, ?, ?)"+
			" ON CONFLICT DO UPDATE SET source = excluded.source"+
			" WHERE event_meta.source <> excluded.source",
	)
	if err != nil {
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	var added int64
	for _, m := range tags {
		if _, dup := present[m]; !dup {
			present[m] = struct{}{}
			added++
		}
		if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value, metaSourceExplicit); err != nil {
			return 0, fmt.Errorf("insert tag: %w", err)
		}
	}

	if err := rebuildEventFTS(ctx, tx, id); err != nil {
		return 0, err
	}

	return added, nil
}

// RemoveTags deletes (event_id, key, value) rows matching tags. Returns
// the number of rows removed. FTS rebuilt in the same transaction.
// Returns ErrNotFound if the event is missing; (0, nil) is a valid
// outcome when none of the tags were present. Empty tags is a no-op.
// Protected keys (`author`) are refused outright, matching DeleteMeta —
// an event with no author renders a blank column everywhere.
func RemoveTags(ctx context.Context, db *sql.DB, id int64, tags []parse.Meta) (int64, error) {
	if len(tags) == 0 {
		return 0, nil
	}
	if err := requireUnprotectedTags("remove", tags); err != nil {
		return 0, err
	}
	return inTx(ctx, db, func(tx *sql.Tx) (int64, error) {
		return removeTagsInTx(ctx, tx, id, tags)
	})
}

// removeTagsInTx is RemoveTags' body.
func removeTagsInTx(ctx context.Context, tx *sql.Tx, id int64, tags []parse.Meta) (int64, error) {
	if err := requireEventExists(ctx, tx, id); err != nil {
		return 0, err
	}

	stmt, err := tx.PrepareContext(ctx,
		"DELETE FROM event_meta WHERE event_id = ? AND key = ? AND value = ?",
	)
	if err != nil {
		return 0, fmt.Errorf("prepare delete: %w", err)
	}
	defer stmt.Close()

	var total int64
	for _, m := range tags {
		res, err := stmt.ExecContext(ctx, id, m.Key, m.Value)
		if err != nil {
			return 0, fmt.Errorf("delete tag: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}
		total += n
	}

	if err := rebuildEventFTS(ctx, tx, id); err != nil {
		return 0, err
	}

	return total, nil
}

func readMetaTx(ctx context.Context, tx *sql.Tx, id int64) ([]parse.Meta, error) {
	rows, err := tx.QueryContext(ctx, "SELECT key, value FROM event_meta WHERE event_id = ? ORDER BY key, value", id)
	if err != nil {
		return nil, fmt.Errorf("query meta: %w", err)
	}
	defer rows.Close()

	var meta []parse.Meta
	for rows.Next() {
		var m parse.Meta
		if err := rows.Scan(&m.Key, &m.Value); err != nil {
			return nil, fmt.Errorf("scan meta: %w", err)
		}
		meta = append(meta, m)
	}
	return meta, rows.Err()
}

// The two halves of Update's body-tag sync. Both are scoped to
// metaSourceBody, which is the point: the delete leaves an operator-added
// tuple of the same key and value standing, and the insert's DO NOTHING
// leaves it explicit rather than demoting it to a mention.
const (
	deleteBodyMetaSQL = "DELETE FROM event_meta" +
		" WHERE event_id = ? AND key = ? AND value = ? AND source = ?"
	insertBodyMetaSQL = "INSERT INTO event_meta (event_id, key, value, source)" +
		" VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING"
)

// execBodyMetaTuples runs query once per tag with (id, key, value, 'body')
// bound. verb names the operation in any error. Empty tags is a no-op.
func execBodyMetaTuples(ctx context.Context, tx *sql.Tx, query, verb string, id int64, tags []parse.Meta) error {
	if len(tags) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return fmt.Errorf("prepare %s: %w", verb, err)
	}
	defer stmt.Close()
	for _, m := range tags {
		if _, err := stmt.ExecContext(ctx, id, m.Key, m.Value, metaSourceBody); err != nil {
			return fmt.Errorf("%s meta: %w", verb, err)
		}
	}
	return nil
}

// UpdateMeta renames every (oldKey, oldValue) tuple across all events to
// (newKey, newValue) and returns the number of tuples it renamed. A rename
// onto a tuple that already exists is a merge, not an error: an event
// carrying both ends up with one, and the row it absorbed is gone (so the
// count is rows renamed, not rows the database gained). Refuses a protected
// key (currently `author`) on either end of a rename that *changes* the key,
// so no rename can break the renderers that look it up by name; correcting a
// protected key's value in place is allowed — see requireRenamableMeta.
func UpdateMeta(ctx context.Context, db *sql.DB, oldKey, oldValue, newKey, newValue string) (int64, error) {
	if err := requireRenamableMeta(oldKey, newKey, newValue); err != nil {
		return 0, err
	}
	// OR REPLACE is what makes consolidating two tags (`#wip` → `#done`)
	// work, and consolidating is the main reason to run this verb. An event
	// carrying both collides with the UNIQUE(key, value, event_id) index
	// migration 2 added; a plain UPDATE aborts on the first such row and
	// rolls the whole rename back, so nothing moves at all. OR REPLACE drops
	// the row in the way and completes the update, which is exactly the
	// merge. Renaming a tuple to itself needs no special case: SQLite checks
	// uniqueness against the *other* rows, so the row is simply rewritten
	// with the values it already had.
	//
	// The renamed row becomes explicit whatever it was before: the new value
	// is one the operator chose and no event's text yields it, so leaving it
	// body-derived would let the next edit of any renamed event delete the
	// rename along with the mention it no longer matches.
	//
	// Rows replaced away are not counted, only rows updated — which is the
	// honest number for "renamed".
	return rewriteMetaTuple(ctx, db, "update", oldKey, oldValue,
		"UPDATE OR REPLACE event_meta SET key = ?, value = ?, source = ? WHERE key = ? AND value = ?",
		newKey, newValue, metaSourceExplicit, oldKey, oldValue,
	)
}

// DeleteMeta removes every (key, value) tuple across all events and
// returns the number of rows deleted. Refuses protected keys for the same
// reason as UpdateMeta.
func DeleteMeta(ctx context.Context, db *sql.DB, key, value string) (int64, error) {
	if err := requireUnprotectedMeta("delete", key); err != nil {
		return 0, err
	}
	return rewriteMetaTuple(ctx, db, "delete", key, value,
		"DELETE FROM event_meta WHERE key = ? AND value = ?", key, value,
	)
}

// rewriteMetaTuple runs one statement over every row carrying the (key, value)
// tuple and resyncs the FTS content of the events that carried it, returning
// the number of rows the statement affected. verb names the operation in any
// error.
//
// It is the shared body of UpdateMeta and DeleteMeta, which differed in the one
// statement and were otherwise the same forty lines — including the ordering
// that makes them correct: the affected ids are captured *before* the statement
// runs, because afterwards there is no tuple left to find them by, and the FTS
// content of an event includes its key=value meta tokens. A third
// mutate-by-tuple verb would have been the third copy of that ordering.
//
// Callers do their own protected-key gate first. It stays out here because the
// two verbs answer it differently — see requireRenamableMeta.
func rewriteMetaTuple(ctx context.Context, db *sql.DB, verb, key, value, query string, args ...any) (int64, error) {
	return inTx(ctx, db, func(tx *sql.Tx) (int64, error) {
		ids, err := metaEventIDs(ctx, tx, key, value)
		if err != nil {
			return 0, err
		}

		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, fmt.Errorf("%s meta: %w", verb, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("rows affected: %w", err)
		}

		for _, id := range ids {
			if err := rebuildEventFTS(ctx, tx, id); err != nil {
				return 0, err
			}
		}
		return n, nil
	})
}

// metaEventIDs returns the distinct event IDs carrying the given (key, value)
// meta tuple. Used by UpdateMeta/DeleteMeta to know which events' FTS content
// needs rebuilding after the tuple is renamed or removed.
func metaEventIDs(ctx context.Context, tx *sql.Tx, key, value string) ([]int64, error) {
	rows, err := tx.QueryContext(ctx,
		"SELECT DISTINCT event_id FROM event_meta WHERE key = ? AND value = ?",
		key, value,
	)
	if err != nil {
		return nil, fmt.Errorf("query meta event ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan meta event id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meta event ids: %w", err)
	}
	return ids, nil
}

// CountMeta returns the number of events carrying the given (key, value)
// meta tuple.
func CountMeta(ctx context.Context, db *sql.DB, key, value string) (int64, error) {
	var n int64
	err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM event_meta WHERE key = ? AND value = ?",
		key, value,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count meta: %w", err)
	}
	return n, nil
}

// MetaCount is one row of a `fngr meta` summary: a (key, value) tuple
// and how many events it appears on.
type MetaCount struct {
	Key   string
	Value string
	Count int
}

// ListMetaOpts narrows the result of ListMeta. Both fields are optional;
// zero values mean "no filter on that field". When both are set, the
// query is an exact (key, value) match.
type ListMetaOpts struct {
	Key   string
	Value string
}

// ListMeta returns one MetaCount row per (key, value) tuple matching opts,
// sorted by key then value.
func ListMeta(ctx context.Context, db *sql.DB, opts ListMetaOpts) ([]MetaCount, error) {
	query := "SELECT key, value, COUNT(*) AS count FROM event_meta"
	var args []any
	var conds []string
	if opts.Key != "" {
		conds = append(conds, "key = ?")
		args = append(args, opts.Key)
	}
	if opts.Value != "" {
		conds = append(conds, "value = ?")
		args = append(args, opts.Value)
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ") // #nosec G202 -- conds are "key = ?" and "value = ?" only, not user input
	}
	query += " GROUP BY key, value ORDER BY key, value"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query meta counts: %w", err)
	}
	defer rows.Close()

	var result []MetaCount
	for rows.Next() {
		var mc MetaCount
		if err := rows.Scan(&mc.Key, &mc.Value, &mc.Count); err != nil {
			return nil, fmt.Errorf("scan meta count: %w", err)
		}
		result = append(result, mc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meta counts: %w", err)
	}

	return result, nil
}
