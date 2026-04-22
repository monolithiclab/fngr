# list UX overhaul Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement S1 of the roadmap — make `fngr` the default command, flip list to newest-first by default with `-r`/`--reverse` for the opposite, switch tree/flat to relative-aware compact timestamps, stream events for flat/csv/json, and pipe through the system pager when stdout is a TTY.

**Architecture:** Three orthogonal pieces (display format in `timefmt`+`render`, sort/streaming in `event`+`render`, pager + default command in `cmd/fngr`) are built layer-by-layer and only wired into `ListCmd` once each layer has its own tests. Tree continues to load all events; flat/csv/json switch to a streamed pull from the database.

**Tech Stack:** Go 1.26 (using `iter.Seq2` from the standard library), Kong for CLI dispatch, `modernc.org/sqlite` for storage, `golang.org/x/term` for TTY detection.

**Spec:** [`docs/superpowers/specs/2026-04-18-list-ux-overhaul-design.md`](../specs/2026-04-18-list-ux-overhaul-design.md)

**Project conventions** (from `CLAUDE.md`):
- Always use `make ci -j8` for the full check; `go test ./pkg/...` is fine while iterating.
- Tests parallel-safe (`t.Parallel()`), table-driven where useful.
- Commit messages are lowercase imperative (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`) with a `Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>` trailer.
- Never commit `REVIEW.md`, `CLAUDE.md`, or `README.md` (the user maintains those manually).

---

## File map

**Created:**
- `cmd/fngr/pager.go` — `withPager` helper
- `cmd/fngr/pager_test.go` — pager unit tests

**Modified:**
- `internal/timefmt/timefmt.go` — layout constants + `FormatRelative`
- `internal/timefmt/timefmt_test.go` — `FormatRelative` table test
- `internal/event/event.go` — `ListOpts.Desc` → `Ascending`; new `ListSeq`; `List` collects via `ListSeq`
- `internal/event/event_test.go` — update for renamed field + new sort default; add `ListSeq` tests
- `internal/event/store.go` — `Store.ListSeq` method
- `internal/event/store_test.go` — direct test for the new method
- `internal/render/render.go` — `formatLocalDate` becomes `formatLocalStamp(t, now)` via `FormatRelative`; new `FlatStream`/`CSVStream`/`JSONStream`/`EventsStream`
- `internal/render/render_test.go` — update fixtures for new compact stamps; new streaming tests
- `cmd/fngr/list.go` — drop `Sort`; add `Reverse` and `NoPager`; new `toListOpts`; branch tree vs streaming
- `cmd/fngr/list_test.go` — update for new flags + add streaming/pager-disabled cases
- `cmd/fngr/main.go` — `default:"withargs"` on `List` so bare `fngr` runs it
- `cmd/fngr/store.go` — `ListSeq` on the `eventStore` interface
- `cmd/fngr/dispatch_test.go` — add bare `fngr`, `fngr -r`, `fngr --no-pager` cases
- `go.mod` / `go.sum` — `golang.org/x/term`

**Not committed (per project policy):** `README.md`, `CLAUDE.md`, `REVIEW.md`.

---

### Task 1: `timefmt.FormatRelative` and layout constants

**Files:**
- Modify: `internal/timefmt/timefmt.go`
- Test: `internal/timefmt/timefmt_test.go`

- [ ] **Step 1: Add the failing test.** Append at the bottom of `internal/timefmt/timefmt_test.go`:

```go
func TestFormatRelative(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 18, 14, 30, 0, 0, time.Local)
	tests := []struct {
		name string
		t    time.Time
		want string
	}{
		{"same instant", now, "2.30pm"},
		{"earlier today", time.Date(2026, 4, 18, 9, 32, 0, 0, time.Local), "9.32am"},
		{"later today", time.Date(2026, 4, 18, 21, 30, 0, 0, time.Local), "9.30pm"},
		{"yesterday this year", time.Date(2026, 4, 17, 23, 59, 0, 0, time.Local), "Apr 17 11.59pm"},
		{"earlier this year", time.Date(2026, 1, 5, 8, 5, 0, 0, time.Local), "Jan 05 8.05am"},
		{"prior year", time.Date(2024, 12, 9, 21, 32, 0, 0, time.Local), "Dec 09 2024 9.32pm"},
		{"midnight today", time.Date(2026, 4, 18, 0, 0, 0, 0, time.Local), "12.00am"},
		{"midnight yesterday", time.Date(2026, 4, 17, 0, 0, 0, 0, time.Local), "Apr 17 12.00am"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FormatRelative(tt.t, now); got != tt.want {
				t.Errorf("FormatRelative(%v, %v) = %q, want %q", tt.t, now, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails.**

Run: `go test ./internal/timefmt/ -run TestFormatRelative -v`
Expected: FAIL with `undefined: FormatRelative`.

- [ ] **Step 3: Implement.** Add to `internal/timefmt/timefmt.go` after the existing `timeOnlyFormats` block (before `Parse`):

```go
const (
	// LayoutToday is used when the event happened on `now`'s local date.
	LayoutToday = "3.04pm"
	// LayoutThisYear is used for events in the same calendar year as `now`
	// but on a different day.
	LayoutThisYear = "Jan 02 3.04pm"
	// LayoutOlder is used for events from a prior calendar year.
	LayoutOlder = "Jan 02 2006 3.04pm"
)

// FormatRelative formats t in the most compact human form that retains the
// information needed to disambiguate from `now`:
//   - same local date as now -> "9.32pm"
//   - same local year as now -> "Dec 09 9.32pm"
//   - older                  -> "Dec 09 2024 9.32pm"
//
// am/pm are emitted lowercase; the time uses '.' as the hour/minute
// separator (e.g. "9.32pm") to match fngr's display convention.
func FormatRelative(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	layout := LayoutOlder
	switch {
	case sameDay(t, now):
		layout = LayoutToday
	case t.Year() == now.Year():
		layout = LayoutThisYear
	}
	out := t.Format(layout)
	// time.Format emits "AM"/"PM"; lowercase only that suffix so month
	// abbreviations stay capitalized.
	return strings.Replace(strings.Replace(out, "AM", "am", 1), "PM", "pm", 1)
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
```

Add `"strings"` to the existing import block at the top.

- [ ] **Step 4: Run the test to confirm it passes.**

Run: `go test ./internal/timefmt/ -run TestFormatRelative -v`
Expected: PASS for all eight subtests.

- [ ] **Step 5: Run the package's full test + lint.**

Run: `go test ./internal/timefmt/...`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/timefmt/timefmt.go internal/timefmt/timefmt_test.go
git commit -m "$(cat <<'EOF'
feat(timefmt): add FormatRelative for compact list stamps

Returns "9.32pm" for events from today, "Dec 09 9.32pm" for events
earlier this year, "Dec 09 2024 9.32pm" for older events. am/pm
lowercased; period used as the hour/minute separator to match the
list display convention. Uses an injected "now" so callers can keep
tests deterministic.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: render uses `FormatRelative` for tree + flat lines

**Files:**
- Modify: `internal/render/render.go`
- Modify: `internal/render/render_test.go`

- [ ] **Step 1: Update tree/flat fixtures to the new format.** In `internal/render/render_test.go`, replace each existing tree/flat expected-output block. Find the `TestTree_FlatList` test and rewrite its `want`:

Replace:
```go
	want := "" +
		"1   2026-04-10  nicolas  First event\n" +
		"2   2026-04-11  nicolas  Second event\n"
```

with (the date column changes from `2026-04-10` to `Apr 10 2026 12.00am`):

```go
	want := "" +
		"1   Apr 10 2026 12.00am  nicolas  First event\n" +
		"2   Apr 11 2026 12.00am  nicolas  Second event\n"
```

Apply the same date → `Apr DD 2026 12.00am` substitution to:

- `TestTree_NestedChildren` (events at 2026-04-10 and 2026-04-11)
- `TestTree_DeepNesting` (same dates)
- `TestTree_MixedRootsAndChildren` (2026-04-10, 2026-04-11, 2026-04-12)
- `TestTree_OrphanedChildren` (2026-04-10, 2026-04-11)
- `TestFlat` (2026-04-10, 2026-04-11)

> Why `Apr DD 2026 12.00am`? `makeEvent` builds events via `time.Parse("2006-01-02", date)` which produces midnight UTC. Tests run "now" through the renderer's `time.Now()`; the fixture dates in 2026 will be older (or matching the current year depending on test execution year, but the fixture sets explicit time-of-day midnight). Once we inject `now` in step 3, we'll use a fixed clock so output is stable regardless of when tests run. Keep this substitution for now and step 3 makes it deterministic.

- [ ] **Step 2: Add a deterministic `now` injection point.** In `internal/render/render.go`, replace `formatLocalDate` and its caller in `formatEventLine` to take a relative anchor.

Find:
```go
func formatLocalDate(t time.Time) string {
	return t.Local().Format(timefmt.DateFormat)
}
```

Replace the function and update `Tree`/`renderNode`/`Flat` to thread the anchor. The minimal diff:

```go
// nowFunc lets tests pin the relative-stamp anchor.
var nowFunc = time.Now

func formatLocalStamp(t time.Time) string {
	return timefmt.FormatRelative(t, nowFunc())
}
```

Delete the now-unused `formatLocalDate` and the unused `timefmt.DateFormat` reference. (`formatLocalDateTime` and `timefmt.DateTimeFormat` stay; they're still used by `Event` detail.)

Update both call sites in `internal/render/render.go`:

```go
// in renderNode (around the line that builds `line`):
line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Text)
```

```go
// in Flat (the loop body):
line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Text)
```

- [ ] **Step 3: Pin `nowFunc` in tests via a helper.** Add to `internal/render/render_test.go` near the existing helpers:

```go
// pinNow forces formatLocalStamp to use a fixed anchor so tree/flat output
// is deterministic across runs and across calendar years.
func pinNow(t *testing.T, now time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = prev })
}
```

In each test that builds expected tree/flat output (the same six tests touched in step 1), call `pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))` at the top of the subtest. With anchor 2030, all 2026 fixture events fall into the "older" bucket → `Apr 10 2026 12.00am`, matching the strings updated in step 1.

Example (`TestTree_FlatList`):
```go
func TestTree_FlatList(t *testing.T) {
	t.Parallel()
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	// ... rest unchanged
```

- [ ] **Step 4: Run render tests.**

Run: `go test ./internal/render/...`
Expected: PASS. If any test fails because the expected string still says `2026-04-10`, finish step 1 for it.

- [ ] **Step 5: Build the rest of the workspace; surface broken callers.**

Run: `go build ./...`
Expected: PASS. (`formatLocalDate` was unexported; no external callers.)

- [ ] **Step 6: Run the full suite.**

Run: `make ci -j8`
Expected: PASS. The CLI dispatch tests do not assert on stamp text (only format dispatch), so they remain green.

- [ ] **Step 7: Commit.**

```bash
git add internal/render/render.go internal/render/render_test.go
git commit -m "$(cat <<'EOF'
refactor(render): use FormatRelative for tree and flat stamps

Tree and flat list lines now show "9.32pm" / "Dec 09 9.32pm" /
"Dec 09 2024 9.32pm" depending on how old the event is. Event detail
keeps full ISO via formatLocalDateTime. A package-level nowFunc lets
tests pin the relative anchor so fixtures are stable.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Flip default sort to newest-first; rename `ListOpts.Desc` to `Ascending`

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/event_test.go`
- Modify: `cmd/fngr/list.go`
- Modify: `cmd/fngr/list_test.go`

- [ ] **Step 1: Update `ListOpts` field.** In `internal/event/event.go`, find:

```go
type ListOpts struct {
	Filter string
	From   *time.Time // inclusive lower bound
	To     *time.Time // exclusive upper bound (compute end-of-day in caller)
	Limit  int        // 0 means no limit
	Desc   bool       // newest first when true; default is oldest first
}
```

Replace with:

```go
type ListOpts struct {
	Filter    string
	From      *time.Time // inclusive lower bound
	To        *time.Time // exclusive upper bound (compute end-of-day in caller)
	Limit     int        // 0 means no limit
	Ascending bool       // oldest first when true; default is newest first
}
```

In the same file, find the SQL ordering block:

```go
	if opts.Desc {
		query += " ORDER BY e.created_at DESC"
	} else {
		query += " ORDER BY e.created_at ASC"
	}
```

Replace with:

```go
	if opts.Ascending {
		query += " ORDER BY e.created_at ASC"
	} else {
		query += " ORDER BY e.created_at DESC"
	}
```

- [ ] **Step 2: Update the event-package test for the new default.** In `internal/event/event_test.go`, find `TestList_LimitAndSort`. Currently it asserts:

```go
	asc, err := List(ctx, database, ListOpts{Limit: 2})
	if err != nil { ... }
	if len(asc) != 2 || asc[0].Text != "evt 0" {
		t.Errorf("asc limit got %d events, first=%q; want 2 starting with 'evt 0'", len(asc), asc[0].Text)
	}

	desc, err := List(ctx, database, ListOpts{Limit: 2, Desc: true})
	if err != nil { ... }
	if len(desc) != 2 || desc[0].Text != "evt 4" {
		t.Errorf("desc limit got %d events, first=%q; want 2 starting with 'evt 4'", len(desc), desc[0].Text)
	}
```

Rewrite both halves so the default is descending and `Ascending: true` is the explicit flip:

```go
	desc, err := List(ctx, database, ListOpts{Limit: 2})
	if err != nil {
		t.Fatalf("List default limit: %v", err)
	}
	if len(desc) != 2 || desc[0].Text != "evt 4" {
		t.Errorf("default limit got %d events, first=%q; want 2 starting with 'evt 4'", len(desc), desc[0].Text)
	}

	asc, err := List(ctx, database, ListOpts{Limit: 2, Ascending: true})
	if err != nil {
		t.Fatalf("List ascending limit: %v", err)
	}
	if len(asc) != 2 || asc[0].Text != "evt 0" {
		t.Errorf("ascending limit got %d events, first=%q; want 2 starting with 'evt 0'", len(asc), asc[0].Text)
	}
```

Also check `TestList_NoFilter` — it asserts `events[0].Text != "first event #work"`. With newest-first default, this becomes the second event. Update those assertions:

```go
	if events[0].Text != "second event #personal" {
		t.Errorf("events[0].Text = %q, want %q", events[0].Text, "second event #personal")
	}
	if events[1].Text != "first event #work" {
		t.Errorf("events[1].Text = %q, want %q", events[1].Text, "first event #work")
	}
```

- [ ] **Step 3: Replace `Sort` flag with `Reverse` in `ListCmd`.** In `cmd/fngr/list.go`, find the struct + `Run`:

```go
type ListCmd struct {
	Filter string `arg:"" optional:"" help:"..."`
	From   string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To     string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format string `help:"..." enum:"tree,flat,json,csv" default:"tree"`
	Limit  int    `help:"Maximum events to return (0 = no limit)." short:"n" default:"0"`
	Sort   string `help:"Sort order: asc (default, oldest first) or desc (newest first)." enum:"asc,desc" default:"asc"`
}
```

Replace the struct (drop `Sort`, add `Reverse`):

```go
type ListCmd struct {
	Filter  string `arg:"" optional:"" help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)."`
	From    string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To      string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format  string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
	Limit   int    `help:"Maximum events to return (0 = no limit)." short:"n" default:"0"`
	Reverse bool   `help:"Sort oldest first (default is newest first)." short:"r"`
}
```

In `Run`, replace:

```go
	opts := event.ListOpts{
		Filter: c.Filter,
		Limit:  c.Limit,
		Desc:   c.Sort == "desc",
	}
```

with:

```go
	opts := event.ListOpts{
		Filter:    c.Filter,
		Limit:     c.Limit,
		Ascending: c.Reverse,
	}
```

- [ ] **Step 4: Update `cmd/fngr/list_test.go`.** Find `TestListCmd_LimitAndSort`:

```go
	cmd := &ListCmd{Format: "flat", Limit: 1, Sort: "desc"}
```

Default sort is now descending; `desc` was the old explicit. Replace with the new flag (and rename the test for clarity):

```go
func TestListCmd_LimitAndDefaultSort(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for _, text := range []string{"alpha", "beta", "gamma"} {
		if _, err := s.Add(context.Background(), text, nil, []parse.Meta{
			{Key: "author", Value: "alice"},
		}, nil); err != nil {
			t.Fatalf("Add %s: %v", text, err)
		}
	}

	cmd := &ListCmd{Format: "flat", Limit: 1}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if strings.Count(got, "\n") != 1 {
		t.Errorf("limit=1 produced %d lines:\n%s", strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, "gamma") {
		t.Errorf("default sort: expected gamma first, got:\n%s", got)
	}
}

func TestListCmd_Reverse(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for _, text := range []string{"alpha", "beta", "gamma"} {
		if _, err := s.Add(context.Background(), text, nil, []parse.Meta{
			{Key: "author", Value: "alice"},
		}, nil); err != nil {
			t.Fatalf("Add %s: %v", text, err)
		}
	}

	cmd := &ListCmd{Format: "flat", Limit: 1, Reverse: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "alpha") {
		t.Errorf("reverse sort: expected alpha first, got:\n%s", got)
	}
}
```

- [ ] **Step 5: Search for any other `Sort` or `Desc` references.**

Run: `grep -rn "Sort\|\.Desc\b\|Desc:\s*true\|Desc:\s*false" cmd/ internal/`
Expected: no matches inside `cmd/fngr/` or `internal/event/` outside the changed files. (Unrelated mentions like `gosec` are fine.)

- [ ] **Step 6: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/event/event.go internal/event/event_test.go cmd/fngr/list.go cmd/fngr/list_test.go
git commit -m "$(cat <<'EOF'
refactor: list defaults to newest first; -r/--reverse flips

Drop ListOpts.Desc in favour of Ascending (zero value = descending =
newest first). The CLI loses --sort and gains -r/--reverse. Tests
follow the new default.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Add `event.ListSeq` and refactor `List` to collect via it

**Files:**
- Modify: `internal/event/event.go`
- Modify: `internal/event/event_test.go`

- [ ] **Step 1: Write the failing tests.** Add to `internal/event/event_test.go` (next to other List tests):

```go
func TestListSeq_YieldsAllInOrder(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	for i := range 3 {
		if _, err := Add(ctx, database, fmt.Sprintf("evt %d", i), nil, []parse.Meta{
			{Key: MetaKeyAuthor, Value: "alice"},
		}, nil); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	var got []string
	for ev, err := range ListSeq(ctx, database, ListOpts{Ascending: true}) {
		if err != nil {
			t.Fatalf("ListSeq: %v", err)
		}
		if len(ev.Meta) != 1 || ev.Meta[0].Value != "alice" {
			t.Errorf("event %d meta = %v, want [{author alice}]", ev.ID, ev.Meta)
		}
		got = append(got, ev.Text)
	}
	want := []string{"evt 0", "evt 1", "evt 2"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestListSeq_AcrossBatchBoundary(t *testing.T) {
	t.Parallel()
	database := testDB(t)

	const n = metaBatchSize + 50
	for i := range n {
		if _, err := Add(ctx, database, fmt.Sprintf("e%d", i), nil, []parse.Meta{
			{Key: MetaKeyAuthor, Value: "alice"},
		}, nil); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	count := 0
	for ev, err := range ListSeq(ctx, database, ListOpts{Ascending: true}) {
		if err != nil {
			t.Fatalf("ListSeq err: %v", err)
		}
		if len(ev.Meta) != 1 {
			t.Fatalf("event %d meta missing across batch boundary", ev.ID)
		}
		count++
	}
	if count != n {
		t.Errorf("yielded %d events, want %d", count, n)
	}
}
```

- [ ] **Step 2: Run the failing tests.**

Run: `go test ./internal/event/ -run "TestListSeq" -v`
Expected: FAIL with `undefined: ListSeq`.

- [ ] **Step 3: Implement `ListSeq` and refactor `List`.** Inside `internal/event/event.go`, locate the existing `List` function. Replace it (and add `ListSeq`) with:

```go
// ListSeq yields events matching opts one at a time, fetching from the
// database and loading metadata in batches of metaBatchSize. The second
// yielded value is the first error encountered; iteration stops after an
// error is yielded.
func ListSeq(ctx context.Context, db *sql.DB, opts ListOpts) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		query, args := buildListQuery(opts)
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			yield(Event{}, fmt.Errorf("query events: %w", err))
			return
		}
		defer rows.Close()

		batch := make([]Event, 0, metaBatchSize)
		flush := func() bool {
			if len(batch) == 0 {
				return true
			}
			if err := loadMetaBatch(ctx, db, batch); err != nil {
				yield(Event{}, err)
				return false
			}
			for _, ev := range batch {
				if !yield(ev, nil) {
					return false
				}
			}
			batch = batch[:0]
			return true
		}

		for rows.Next() {
			var e Event
			var parentID sql.NullInt64
			if err := rows.Scan(&e.ID, &parentID, &e.Text, &e.CreatedAt); err != nil {
				yield(Event{}, fmt.Errorf("scan event: %w", err))
				return
			}
			if parentID.Valid {
				e.ParentID = &parentID.Int64
			}
			batch = append(batch, e)
			if len(batch) >= metaBatchSize {
				if !flush() {
					return
				}
			}
		}
		if err := rows.Err(); err != nil {
			yield(Event{}, fmt.Errorf("iterate events: %w", err))
			return
		}
		flush()
	}
}

// List collects every event from ListSeq. Use ListSeq directly when you can
// stream (flat/csv/json renderers); use List when you genuinely need the
// full slice in memory (tree topology, GetSubtree).
func List(ctx context.Context, db *sql.DB, opts ListOpts) ([]Event, error) {
	var out []Event
	for ev, err := range ListSeq(ctx, db, opts) {
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

func buildListQuery(opts ListOpts) (string, []any) {
	var query string
	var args []any

	if opts.Filter != "" {
		matchExpr := preprocessFilter(opts.Filter)
		if positiveExpr, ok := strings.CutPrefix(matchExpr, "NOT "); ok {
			query = `SELECT e.id, e.parent_id, e.text, e.created_at
				FROM events e
				WHERE e.id NOT IN (
					SELECT rowid FROM events_fts WHERE events_fts MATCH ?
				)`
			args = append(args, positiveExpr)
		} else {
			query = `SELECT e.id, e.parent_id, e.text, e.created_at
				FROM events e
				JOIN events_fts f ON f.rowid = e.id
				WHERE events_fts MATCH ?`
			args = append(args, matchExpr)
		}
	} else {
		query = `SELECT e.id, e.parent_id, e.text, e.created_at
			FROM events e
			WHERE 1=1`
	}

	if opts.From != nil {
		query += " AND e.created_at >= ?"
		args = append(args, opts.From.UTC().Format(timefmt.DateTimeFormat))
	}
	if opts.To != nil {
		query += " AND e.created_at < ?"
		args = append(args, opts.To.UTC().Format(timefmt.DateTimeFormat))
	}

	if opts.Ascending {
		query += " ORDER BY e.created_at ASC"
	} else {
		query += " ORDER BY e.created_at DESC"
	}
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}

	return query, args
}
```

Add `"iter"` to the import block at the top of `internal/event/event.go`.

- [ ] **Step 4: Run the new tests.**

Run: `go test ./internal/event/ -run "TestListSeq" -v`
Expected: PASS for both subtests.

- [ ] **Step 5: Run all event tests.**

Run: `go test ./internal/event/...`
Expected: PASS.

- [ ] **Step 6: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/event/event.go internal/event/event_test.go
git commit -m "$(cat <<'EOF'
feat(event): add streaming ListSeq, collect List on top of it

ListSeq yields events as iter.Seq2[Event, error], loading metadata in
500-event batches via the existing loadMetaBatch. List becomes a thin
collector so callers that genuinely need the full slice (tree, future
GetSubtree consumers) keep working unchanged.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Add `Store.ListSeq` with a direct test

**Files:**
- Modify: `internal/event/store.go`
- Modify: `internal/event/store_test.go`

- [ ] **Step 1: Write the failing test.** Append to `internal/event/store_test.go`:

```go
func TestStore_ListSeq(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)

	for i := range 3 {
		if _, err := s.Add(ctx, fmt.Sprintf("e%d", i), nil, nil, nil); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	var got []string
	for ev, err := range s.ListSeq(ctx, ListOpts{Ascending: true}) {
		if err != nil {
			t.Fatalf("ListSeq: %v", err)
		}
		got = append(got, ev.Text)
	}
	want := []string{"e0", "e1", "e2"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("got %v, want %v", got, want)
	}
}
```

Add `"fmt"` to the import block of `internal/event/store_test.go` if it's not already imported.

- [ ] **Step 2: Run the test to confirm it fails.**

Run: `go test ./internal/event/ -run TestStore_ListSeq -v`
Expected: FAIL with `s.ListSeq undefined`.

- [ ] **Step 3: Implement.** Add to `internal/event/store.go` (next to the other store methods, e.g. after `List`):

```go
func (s *Store) ListSeq(ctx context.Context, opts ListOpts) iter.Seq2[Event, error] {
	return ListSeq(ctx, s.DB, opts)
}
```

Add `"iter"` to the import block of `internal/event/store.go`.

- [ ] **Step 4: Run the test.**

Run: `go test ./internal/event/ -run TestStore_ListSeq -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add internal/event/store.go internal/event/store_test.go
git commit -m "$(cat <<'EOF'
feat(event): add Store.ListSeq wrapping ListSeq

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Streaming renderers — `FlatStream`, `CSVStream`, `JSONStream`, `EventsStream`

**Files:**
- Modify: `internal/render/render.go`
- Modify: `internal/render/render_test.go`

- [ ] **Step 1: Write the failing tests.** Append to `internal/render/render_test.go`:

```go
import "iter"  // add to existing import block, do not duplicate

func staticSeq(events []event.Event) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func errorAtSeq(events []event.Event, errAt int, err error) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		for i, ev := range events {
			if i == errAt {
				yield(event.Event{}, err)
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func TestFlatStream_MatchesFlat(t *testing.T) {
	t.Parallel()
	pinNow(t, time.Date(2030, 1, 1, 0, 0, 0, 0, time.Local))
	events := []event.Event{
		makeEvent(1, nil, "first", "2026-04-10", "alice"),
		makeEvent(2, nil, "second", "2026-04-11", "alice"),
	}

	var slow, fast bytes.Buffer
	if err := Flat(&slow, events); err != nil {
		t.Fatalf("Flat: %v", err)
	}
	if err := FlatStream(&fast, staticSeq(events)); err != nil {
		t.Fatalf("FlatStream: %v", err)
	}
	if slow.String() != fast.String() {
		t.Errorf("FlatStream != Flat\n--- Flat ---\n%s\n--- Stream ---\n%s", slow.String(), fast.String())
	}
}

func TestCSVStream_MatchesCSV(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "x", "2026-04-10", "alice"),
		makeEvent(2, nil, "y", "2026-04-11", "alice"),
	}

	var slow, fast bytes.Buffer
	if err := CSV(&slow, events); err != nil {
		t.Fatalf("CSV: %v", err)
	}
	if err := CSVStream(&fast, staticSeq(events)); err != nil {
		t.Fatalf("CSVStream: %v", err)
	}
	if slow.String() != fast.String() {
		t.Errorf("CSVStream != CSV\n--- CSV ---\n%s\n--- Stream ---\n%s", slow.String(), fast.String())
	}
}

func TestJSONStream_ProducesValidJSON(t *testing.T) {
	t.Parallel()
	events := []event.Event{
		makeEvent(1, nil, "a", "2026-04-10", "alice"),
		makeEvent(2, nil, "b", "2026-04-11", "alice"),
	}

	var b bytes.Buffer
	if err := JSONStream(&b, staticSeq(events)); err != nil {
		t.Fatalf("JSONStream: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal(b.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\noutput:\n%s", err, b.String())
	}
	if len(parsed) != 2 {
		t.Errorf("got %d entries, want 2; output:\n%s", len(parsed), b.String())
	}
	if !strings.HasSuffix(b.String(), "\n") {
		t.Error("JSONStream missing trailing newline")
	}
}

func TestJSONStream_EmptyProducesEmptyArray(t *testing.T) {
	t.Parallel()
	var b bytes.Buffer
	if err := JSONStream(&b, staticSeq(nil)); err != nil {
		t.Fatalf("JSONStream: %v", err)
	}
	got := strings.TrimSpace(b.String())
	if got != "[]" {
		t.Errorf("empty stream produced %q, want %q", got, "[]")
	}
}

func TestJSONStream_ClosesOnError(t *testing.T) {
	t.Parallel()
	events := []event.Event{makeEvent(1, nil, "ok", "2026-04-10", "alice")}
	wantErr := errors.New("boom")

	var b bytes.Buffer
	err := JSONStream(&b, errorAtSeq(events, 1, wantErr))
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want boom", err)
	}
	if !strings.HasSuffix(strings.TrimSpace(b.String()), "]") {
		t.Errorf("JSONStream did not close array; got:\n%s", b.String())
	}
}

func TestEventsStream_Dispatch(t *testing.T) {
	t.Parallel()
	events := []event.Event{makeEvent(1, nil, "x", "2026-04-10", "alice")}

	tests := []struct {
		format string
		check  func(string) bool
	}{
		{"flat", func(s string) bool { return strings.Contains(s, "x") }},
		{"json", func(s string) bool { return strings.HasPrefix(s, "[") }},
		{"csv", func(s string) bool { return strings.HasPrefix(s, "id,parent_id,") }},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			t.Parallel()
			var b bytes.Buffer
			if err := EventsStream(&b, tt.format, staticSeq(events)); err != nil {
				t.Fatalf("EventsStream(%q): %v", tt.format, err)
			}
			if !tt.check(b.String()) {
				t.Errorf("EventsStream(%q) unexpected output:\n%s", tt.format, b.String())
			}
		})
	}
}

func TestEventsStream_RejectsTree(t *testing.T) {
	t.Parallel()
	if err := EventsStream(io.Discard, "tree", staticSeq(nil)); err == nil {
		t.Error("EventsStream(tree, ...) expected an error")
	}
}
```

Add to existing import block of `internal/render/render_test.go`: `"errors"`, `"io"`. (`encoding/json`, `bytes`, `strings`, `time`, `testing`, `event`, `parse` are already there.)

- [ ] **Step 2: Run the failing tests.**

Run: `go test ./internal/render/ -run "Stream|EventsStream" -v`
Expected: FAIL with several `undefined` errors.

- [ ] **Step 3: Implement the streaming renderers.** Append to `internal/render/render.go`:

```go
// FlatStream is the streaming counterpart to Flat. It writes one line per
// event as the iterator yields. The first error from seq aborts and is
// returned.
func FlatStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	for ev, err := range seq {
		if err != nil {
			return err
		}
		line := formatEventLine(ev.ID, formatLocalStamp(ev.CreatedAt), eventAuthor(ev), ev.Text)
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}

// CSVStream is the streaming counterpart to CSV. It writes the header
// followed by one row per event from seq.
func CSVStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"id", "parent_id", "created_at", "author", "text"}); err != nil {
		return err
	}
	for ev, err := range seq {
		if err != nil {
			cw.Flush()
			return err
		}
		parentID := ""
		if ev.ParentID != nil {
			parentID = strconv.FormatInt(*ev.ParentID, 10)
		}
		if err := cw.Write([]string{
			strconv.FormatInt(ev.ID, 10),
			parentID,
			ev.CreatedAt.UTC().Format(time.RFC3339),
			eventAuthor(ev),
			ev.Text,
		}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// JSONStream is the streaming counterpart to JSON. It writes a JSON array
// where each element is encoded individually, so the full serialized blob
// is never held in memory. On error mid-stream the array is still closed
// with "]\n" so any captured output is syntactically valid JSON.
func JSONStream(w io.Writer, seq iter.Seq2[event.Event, error]) error {
	if _, err := fmt.Fprint(w, "[\n"); err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("  ", "  ")

	first := true
	var streamErr error
	for ev, err := range seq {
		if err != nil {
			streamErr = err
			break
		}
		if !first {
			if _, werr := fmt.Fprint(w, ",\n  "); werr != nil {
				return werr
			}
		} else {
			if _, werr := fmt.Fprint(w, "  "); werr != nil {
				return werr
			}
			first = false
		}
		if err := enc.Encode(toJSONEvent(ev)); err != nil {
			return err
		}
	}
	// json.Encoder.Encode appends a trailing newline; strip it so the
	// closing "]" sits on its own line cleanly.
	if _, err := fmt.Fprint(w, "]\n"); err != nil {
		return err
	}
	return streamErr
}

// EventsStream dispatches a streaming render. Tree is rejected because it
// requires the full slice for parent-child topology; callers that want
// tree must use Events with a materialized []Event.
func EventsStream(w io.Writer, format string, seq iter.Seq2[event.Event, error]) error {
	switch format {
	case "csv":
		return CSVStream(w, seq)
	case "json":
		return JSONStream(w, seq)
	case "flat":
		return FlatStream(w, seq)
	case "tree":
		return fmt.Errorf("EventsStream: tree format requires the full slice; use Events instead")
	default:
		return FlatStream(w, seq)
	}
}
```

This needs the existing `jsonEvent` struct to be reachable; refactor `JSON` to share a helper. Inside `internal/render/render.go`, change the existing `JSON` function:

Find:

```go
func JSON(w io.Writer, events []event.Event) error {
	out := make([]jsonEvent, len(events))
	for i, ev := range events {
		meta := make(map[string][]string)
		for _, m := range ev.Meta {
			meta[m.Key] = append(meta[m.Key], m.Value)
		}
		out[i] = jsonEvent{
			ID:        ev.ID,
			ParentID:  ev.ParentID,
			Text:      ev.Text,
			CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
			Meta:      meta,
		}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}
```

Replace with:

```go
func toJSONEvent(ev event.Event) jsonEvent {
	meta := make(map[string][]string)
	for _, m := range ev.Meta {
		meta[m.Key] = append(meta[m.Key], m.Value)
	}
	return jsonEvent{
		ID:        ev.ID,
		ParentID:  ev.ParentID,
		Text:      ev.Text,
		CreatedAt: ev.CreatedAt.UTC().Format(time.RFC3339),
		Meta:      meta,
	}
}

func JSON(w io.Writer, events []event.Event) error {
	out := make([]jsonEvent, len(events))
	for i, ev := range events {
		out[i] = toJSONEvent(ev)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}
```

Add `"iter"` to the import block of `internal/render/render.go`.

- [ ] **Step 4: Run the streaming tests.**

Run: `go test ./internal/render/ -run "Stream|EventsStream" -v`
Expected: PASS.

- [ ] **Step 5: Run the package's full test.**

Run: `go test ./internal/render/...`
Expected: PASS (the existing JSON test still validates the buffered path).

- [ ] **Step 6: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add internal/render/render.go internal/render/render_test.go
git commit -m "$(cat <<'EOF'
feat(render): streaming Flat/CSV/JSON renderers + EventsStream dispatch

Each streamer consumes iter.Seq2[Event, error] and writes incrementally so
the full result set never materializes. JSONStream emits valid array
markers even when the iterator yields an error mid-stream. Tree stays on
the buffered path because it needs the topology in memory.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Add `ListSeq` to `eventStore`; switch ListCmd to streaming for non-tree formats

**Files:**
- Modify: `cmd/fngr/store.go`
- Modify: `cmd/fngr/list.go`
- Modify: `cmd/fngr/list_test.go`

- [ ] **Step 1: Extend the interface.** In `cmd/fngr/store.go`, find the `eventStore` interface. Add:

```go
import (
	"context"
	"io"
	"iter"
	"time"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/parse"
)

type eventStore interface {
	Add(ctx context.Context, text string, parentID *int64, meta []parse.Meta, createdAt *time.Time) (int64, error)
	Get(ctx context.Context, id int64) (*event.Event, error)
	Delete(ctx context.Context, id int64) error
	Update(ctx context.Context, id int64, text *string, createdAt *time.Time) error
	HasChildren(ctx context.Context, id int64) (bool, error)
	List(ctx context.Context, opts event.ListOpts) ([]event.Event, error)
	ListSeq(ctx context.Context, opts event.ListOpts) iter.Seq2[event.Event, error]
	GetSubtree(ctx context.Context, rootID int64) ([]event.Event, error)
	ListMeta(ctx context.Context) ([]event.MetaCount, error)
	CountMeta(ctx context.Context, key, value string) (int64, error)
	UpdateMeta(ctx context.Context, oldKey, oldValue, newKey, newValue string) (int64, error)
	DeleteMeta(ctx context.Context, key, value string) (int64, error)
}
```

(Add `"iter"` to the imports if not already there.)

- [ ] **Step 2: Update `ListCmd.Run` to branch on tree vs streaming + factor `toListOpts`.** Replace `cmd/fngr/list.go` in full:

```go
package main

import (
	"context"
	"fmt"

	"github.com/monolithiclab/fngr/internal/event"
	"github.com/monolithiclab/fngr/internal/render"
	"github.com/monolithiclab/fngr/internal/timefmt"
)

type ListCmd struct {
	Filter  string `arg:"" optional:"" help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)."`
	From    string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To      string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format  string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
	Limit   int    `help:"Maximum events to return (0 = no limit)." short:"n" default:"0"`
	Reverse bool   `help:"Sort oldest first (default is newest first)." short:"r"`
}

func (c *ListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	opts, err := c.toListOpts()
	if err != nil {
		return err
	}

	if c.Format == "tree" {
		events, err := s.List(ctx, opts)
		if err != nil {
			return err
		}
		return render.Tree(io.Out, events)
	}
	return render.EventsStream(io.Out, c.Format, s.ListSeq(ctx, opts))
}

func (c *ListCmd) toListOpts() (event.ListOpts, error) {
	opts := event.ListOpts{
		Filter:    c.Filter,
		Limit:     c.Limit,
		Ascending: c.Reverse,
	}
	if c.From != "" {
		from, err := timefmt.ParseDate(c.From)
		if err != nil {
			return opts, fmt.Errorf("--from: %w", err)
		}
		opts.From = &from
	}
	if c.To != "" {
		to, err := timefmt.ParseDate(c.To)
		if err != nil {
			return opts, fmt.Errorf("--to: %w", err)
		}
		end := to.AddDate(0, 0, 1)
		opts.To = &end
	}
	return opts, nil
}
```

- [ ] **Step 3: Add a streaming-path test alongside the existing list tests.** Append to `cmd/fngr/list_test.go`:

```go
func TestListCmd_JSONUsesStreamingPath(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	for i := range 3 {
		if _, err := s.Add(context.Background(), fmt.Sprintf("e%d", i), nil, []parse.Meta{
			{Key: "author", Value: "alice"},
		}, nil); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}

	cmd := &ListCmd{Format: "json"}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var parsed []map[string]any
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\noutput:\n%s", err, out.String())
	}
	if len(parsed) != 3 {
		t.Errorf("got %d entries, want 3; output:\n%s", len(parsed), out.String())
	}
}
```

Add `"fmt"` to the import block at the top of `cmd/fngr/list_test.go` if not already imported.

- [ ] **Step 4: Run the list tests.**

Run: `go test ./cmd/fngr/ -run "ListCmd" -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
git add cmd/fngr/store.go cmd/fngr/list.go cmd/fngr/list_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): list streams non-tree formats via EventsStream

ListCmd splits into a tree branch (collected slice) and a streaming
branch (flat/csv/json). toListOpts pulls flag-to-opts conversion out of
Run so the branches stay readable. The eventStore interface gains
ListSeq.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: TTY pager helper

**Files:**
- Create: `cmd/fngr/pager.go`
- Create: `cmd/fngr/pager_test.go`
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency.**

Run: `go get golang.org/x/term@latest`
Expected: `go.mod` and `go.sum` updated; no error.

- [ ] **Step 2: Write the failing tests.** Create `cmd/fngr/pager_test.go`:

```go
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithPager_DisabledNoOps(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	io := ioStreams{In: strings.NewReader(""), Out: &out}

	wrapped, closer := withPager(io, true)
	if wrapped.Out != &out {
		t.Errorf("wrapped.Out should be the original buffer when disabled")
	}
	if err := closer(); err != nil {
		t.Errorf("disabled closer returned error: %v", err)
	}
}

func TestWithPager_NonTTYOutNoOps(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	io := ioStreams{In: strings.NewReader(""), Out: &out}

	wrapped, closer := withPager(io, false)
	if wrapped.Out != &out {
		t.Errorf("wrapped.Out should be the original buffer when Out has no fd")
	}
	if err := closer(); err != nil {
		t.Errorf("no-op closer returned error: %v", err)
	}
}

func TestWithPager_PipesToPagerProcess(t *testing.T) {
	dir := t.TempDir()
	captured := filepath.Join(dir, "captured.txt")

	// Fake pager: dump stdin to a file we can read.
	script := filepath.Join(dir, "fake-pager.sh")
	body := "#!/bin/sh\ncat > " + captured + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake pager: %v", err)
	}
	t.Setenv("PAGER", script)

	// Use os.Stdout so withPager's TTY/fd check sees a real *os.File.
	// We also need the function to think it's a TTY; provide a pty? Too
	// heavyweight. Instead, exercise the helper directly via newPagerCmd.
	cmd, in, err := newPagerCmd()
	if err != nil {
		t.Fatalf("newPagerCmd: %v", err)
	}
	if _, err := io.WriteString(in, "hello pager\n"); err != nil {
		t.Fatalf("write to pager: %v", err)
	}
	if err := in.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("pager wait: %v", err)
	}
	got, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("read captured: %v", err)
	}
	if string(got) != "hello pager\n" {
		t.Errorf("captured = %q, want %q", string(got), "hello pager\n")
	}
}

func TestWithPager_PagerFailureFallsBack(t *testing.T) {
	t.Parallel()
	t.Setenv("PAGER", "/no/such/pager-binary-that-cannot-exist-xyz")

	_, _, err := newPagerCmd()
	if !errors.Is(err, errPagerStartFailed) {
		t.Errorf("err = %v, want errPagerStartFailed", err)
	}
}
```

- [ ] **Step 3: Run the failing tests.**

Run: `go test ./cmd/fngr/ -run "TestWithPager|TestNewPager" -v`
Expected: FAIL with `undefined: withPager`, `undefined: newPagerCmd`, `undefined: errPagerStartFailed`.

- [ ] **Step 4: Implement.** Create `cmd/fngr/pager.go`:

```go
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// errPagerStartFailed signals that the pager process could not be started.
// Callers fall back to direct stdout when this fires.
var errPagerStartFailed = errors.New("pager start failed")

// withPager returns an ioStreams whose Out is the stdin of the user's pager
// (if stdout is a TTY and disabled is false) plus a closer the caller MUST
// defer. The closer waits for the pager to exit so output flushes before
// the process returns.
//
// When stdout isn't a TTY, when disabled is true, when io.Out isn't an
// *os.File, or when the pager fails to start, withPager logs (only on
// genuine start failure) and returns the original io with a no-op closer.
func withPager(io ioStreams, disabled bool) (ioStreams, func() error) {
	if disabled {
		return io, noopCloser
	}
	f, ok := io.Out.(*os.File)
	if !ok {
		return io, noopCloser
	}
	if !term.IsTerminal(int(f.Fd())) {
		return io, noopCloser
	}
	cmd, in, err := newPagerCmd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not start pager: %v\n", err)
		return io, noopCloser
	}
	return ioStreams{In: io.In, Out: in}, func() error {
		_ = in.Close()
		return cmd.Wait()
	}
}

func noopCloser() error { return nil }

// newPagerCmd starts the user's pager and returns the running command plus
// a writer connected to its stdin. Tokenization of $PAGER is by space; a
// $PAGER value with spaces inside quotes is not supported (consistent with
// the spec).
func newPagerCmd() (*exec.Cmd, io.WriteCloser, error) {
	parts := pagerCommand()
	cmd := exec.Command(parts[0], parts[1:]...) // #nosec G204 -- pager comes from $PAGER, an explicit user choice.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errPagerStartFailed, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", errPagerStartFailed, err)
	}
	return cmd, in, nil
}

func pagerCommand() []string {
	if s := strings.TrimSpace(os.Getenv("PAGER")); s != "" {
		return strings.Fields(s)
	}
	return []string{"less", "-FRX"}
}
```

- [ ] **Step 5: Run the new tests.**

Run: `go test ./cmd/fngr/ -run "TestWithPager|TestNewPager" -v`
Expected: PASS for all four. (`TestWithPager_PipesToPagerProcess` is not parallel because it relies on `t.Setenv`, which is only safe outside of `t.Parallel()`.)

- [ ] **Step 6: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 7: Commit.**

```bash
git add cmd/fngr/pager.go cmd/fngr/pager_test.go go.mod go.sum
git commit -m "$(cat <<'EOF'
feat(cmd): add withPager + newPagerCmd helpers

When stdout is a TTY (and the user hasn't opted out), withPager wraps
io.Out so writes go to a pipe that feeds $PAGER (fallback "less -FRX").
The returned closer waits for the pager to drain. Falls back to direct
stdout if the pager binary is missing or io.Out has no fd.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Wire `withPager` into `ListCmd` and add `--no-pager`

**Files:**
- Modify: `cmd/fngr/list.go`
- Modify: `cmd/fngr/list_test.go`

- [ ] **Step 1: Add `NoPager` and call `withPager` in `Run`.** In `cmd/fngr/list.go`, update the struct and `Run`:

Find:
```go
type ListCmd struct {
	Filter  string `arg:"" optional:"" help:"..."`
	From    string `help:"..." placeholder:"YYYY-MM-DD"`
	To      string `help:"..." placeholder:"YYYY-MM-DD"`
	Format  string `help:"..." enum:"tree,flat,json,csv" default:"tree"`
	Limit   int    `help:"..." short:"n" default:"0"`
	Reverse bool   `help:"..." short:"r"`
}
```

Append a `NoPager` field:

```go
type ListCmd struct {
	Filter  string `arg:"" optional:"" help:"Filter expression (#tag, @person, key=value, bare words). Operators: & (AND), | (OR), ! (NOT)."`
	From    string `help:"Start date (inclusive)." placeholder:"YYYY-MM-DD"`
	To      string `help:"End date (inclusive)." placeholder:"YYYY-MM-DD"`
	Format  string `help:"Output format: tree (default), flat, json, csv." enum:"tree,flat,json,csv" default:"tree"`
	Limit   int    `help:"Maximum events to return (0 = no limit)." short:"n" default:"0"`
	Reverse bool   `help:"Sort oldest first (default is newest first)." short:"r"`
	NoPager bool   `help:"Disable the pager even when stdout is a TTY."`
}
```

In `Run`, wrap io with `withPager` first:

```go
func (c *ListCmd) Run(s eventStore, io ioStreams) error {
	ctx := context.Background()

	io, closePager := withPager(io, c.NoPager)
	defer func() {
		if err := closePager(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: pager exited with error: %v\n", err)
		}
	}()

	opts, err := c.toListOpts()
	if err != nil {
		return err
	}

	if c.Format == "tree" {
		events, err := s.List(ctx, opts)
		if err != nil {
			return err
		}
		return render.Tree(io.Out, events)
	}
	return render.EventsStream(io.Out, c.Format, s.ListSeq(ctx, opts))
}
```

Add `"os"` to the import block of `cmd/fngr/list.go`.

- [ ] **Step 2: Add a regression test confirming `--no-pager` is a no-op for non-TTY tests.** Append to `cmd/fngr/list_test.go`:

```go
func TestListCmd_NoPagerStillRendersToBuffer(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	io, out := newTestIO("")

	if _, err := s.Add(context.Background(), "evt", nil, []parse.Meta{
		{Key: "author", Value: "alice"},
	}, nil); err != nil {
		t.Fatalf("Add: %v", err)
	}

	cmd := &ListCmd{Format: "flat", NoPager: true}
	if err := cmd.Run(s, io); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "evt") {
		t.Errorf("expected 'evt' in output, got %q", out.String())
	}
}
```

- [ ] **Step 3: Run list tests.**

Run: `go test ./cmd/fngr/ -run "ListCmd" -v`
Expected: PASS.

- [ ] **Step 4: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 5: Commit.**

```bash
git add cmd/fngr/list.go cmd/fngr/list_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): list pipes through pager on TTY; --no-pager opt-out

ListCmd.Run wraps io.Out with withPager before rendering. Closure
errors from the pager are reported to stderr but never promoted to
the command's exit code (matches git's behaviour). --no-pager
disables the wrap entirely.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Default command — bare `fngr` runs `list`

**Files:**
- Modify: `cmd/fngr/main.go`
- Modify: `cmd/fngr/dispatch_test.go`

- [ ] **Step 1: Add the failing dispatch case.** In `cmd/fngr/dispatch_test.go`, find the `cases` slice in `TestKongDispatch_AllCommands` and add a row:

```go
		{name: "bare-fngr", argv: []string{}, want: ""},
		{name: "bare-fngr-reverse", argv: []string{"-r"}, want: ""},
		{name: "bare-fngr-no-pager", argv: []string{"--no-pager"}, want: ""},
```

(Place these at the start of the slice; they're the new defaults users will hit first.)

- [ ] **Step 2: Run the failing test.**

Run: `go test ./cmd/fngr/ -run TestKongDispatch_AllCommands/bare-fngr -v`
Expected: FAIL — Kong rejects an empty argv because no default command is configured.

- [ ] **Step 3: Mark `List` as the default subcommand.** In `cmd/fngr/main.go`, replace the `CLI` struct's `List` field tag:

Find:
```go
	List   ListCmd   `cmd:"" help:"List events."`
```

Replace with:
```go
	List   ListCmd   `cmd:"" default:"withargs" help:"List events (default command)."`
```

`default:"withargs"` tells Kong to treat `List` as the default command and to keep parsing remaining args as `list`'s positional/flag args.

- [ ] **Step 4: Run the dispatch tests.**

Run: `go test ./cmd/fngr/ -run TestKongDispatch -v`
Expected: PASS for all subtests, including the three new bare-fngr variants.

- [ ] **Step 5: Run the full suite.**

Run: `make ci -j8`
Expected: PASS.

- [ ] **Step 6: Smoke-test the binary.**

Run:
```bash
make build
./build/fngr --db /tmp/fngr-s1.db add "smoke test" --author tester
./build/fngr --db /tmp/fngr-s1.db
./build/fngr --db /tmp/fngr-s1.db -r --format flat
./build/fngr --db /tmp/fngr-s1.db --no-pager --format json
rm /tmp/fngr-s1.db
```

Expected:
- `Added event 1`
- Tree output containing `smoke test` with the new compact stamp.
- Flat output sorted oldest-first.
- JSON array printed without paging.

- [ ] **Step 7: Commit.**

```bash
git add cmd/fngr/main.go cmd/fngr/dispatch_test.go
git commit -m "$(cat <<'EOF'
feat(cmd): bare fngr runs list (default command)

Mark ListCmd as default:"withargs" so "fngr", "fngr -r" and similar
dispatch to ListCmd. The dispatch test guards bare invocation,
--reverse, and --no-pager so wiring regressions surface via the same
TestKongDispatch_AllCommands matrix.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Update README and CLAUDE.md (uncommitted)

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`

> **Important:** Never commit `README.md` or `CLAUDE.md`. The user maintains them. Update them in the working tree only.

- [ ] **Step 1: Update README's Quick start block.** Open `README.md` and find:

```
# List everything (tree view is default)
fngr list

# Filter with tags, people, bare words
fngr list "#ops"
```

Replace that whole block (down to "Pagination and sort order") with:

```
# Default command — list everything (newest first, tree view, paginated on TTY)
fngr

# Explicit subcommand (same behaviour)
fngr list

# Filter with tags, people, bare words
fngr "#ops"
fngr "@sarah & #ops"
fngr "deploy | rollback"
fngr "!#bugfix"

# Date ranges
fngr --from 2026-04-01 --to 2026-04-15

# Pagination and sort order
fngr -n 20             # at most 20 events
fngr -r                # oldest first (default is newest first)
fngr --no-pager        # don't pipe through $PAGER even on a TTY
```

- [ ] **Step 2: Verify in working tree only.**

Run: `git status`
Expected: `README.md` shows as modified but stays untracked / uncommitted.

- [ ] **Step 3: Update CLAUDE.md architecture entries** to mention the new pieces. In `CLAUDE.md`, find the `internal/event/event.go` bullet:

```
- `internal/event/event.go` — Data access functions: `Add` (transactional event + meta + FTS),
  `Get`, `Update` (text and/or timestamp; refreshes FTS from text + existing meta), `Delete`,
  `HasChildren`, `List` (FTS5 filter + date range + `Limit` + `Desc`), `GetSubtree` (recursive
  CTE), ...
```

Edit `Desc` → `Ascending` and add `ListSeq`:

```
- `internal/event/event.go` — Data access functions: `Add` (transactional event + meta + FTS),
  `Get`, `Update` (text and/or timestamp; refreshes FTS from text + existing meta), `Delete`,
  `HasChildren`, `List` / `ListSeq` (FTS5 filter + date range + `Limit` + `Ascending`;
  `ListSeq` is iter.Seq2 streaming, `List` collects on top of it), `GetSubtree` (recursive
  CTE), ...
```

Find the `internal/render/render.go` bullet:

```
- `internal/render/render.go` — Output rendering to `io.Writer`. `Events(w, format, events)` and
  `SingleEvent(w, format, ev)` are the dispatchers commands call; ...
```

Add a sentence about the streaming dispatchers:

```
- `internal/render/render.go` — Output rendering to `io.Writer`. `Events(w, format, events)`,
  `SingleEvent(w, format, ev)`, and `EventsStream(w, format, seq)` are the dispatchers
  commands call; `Tree`, `Flat`/`FlatStream`, `JSON`/`JSONStream`, `CSV`/`CSVStream`, `Event`
  are the underlying writers. List/flat use a relative-aware compact stamp via
  `timefmt.FormatRelative`; event detail keeps full ISO.
```

Add a new bullet between the `cmd/fngr/prompt.go` and `internal/db/db.go` lines:

```
- `cmd/fngr/pager.go` — `withPager(io, disabled) (ioStreams, closer)` wraps Out in a pipe to
  `$PAGER` (fallback `less -FRX`) when stdout is a TTY. Used by `list`.
```

- [ ] **Step 4: Final smoke-check on the suite.**

Run: `make ci -j8`
Expected: PASS, total coverage in line with previous runs (≥ 80%).

(No commit — `README.md` and `CLAUDE.md` stay uncommitted by project policy.)

---

## Self-review

Spec coverage:

- Default command (`fngr` ≡ `fngr list`) → Task 10.
- Sort default flip + `-r`/`--reverse` + drop `--sort` → Task 3.
- Compact relative stamp + `LayoutToday`/`LayoutThisYear`/`LayoutOlder` → Tasks 1, 2.
- Event detail keeps full ISO → confirmed unchanged in Task 2 (we only edit `formatLocalDate`/`formatLocalStamp`; `formatLocalDateTime` is untouched).
- `ListSeq` + 500-event batches + meta loading per batch → Task 4.
- `Store.ListSeq` + interface entry → Tasks 5, 7.
- `FlatStream`/`CSVStream`/`JSONStream` + `EventsStream` dispatcher → Task 6.
- JSON streaming closes `]` on mid-stream error → Task 6 (test `TestJSONStream_ClosesOnError`).
- TTY pager via `$PAGER` fallback `less -FRX` + `--no-pager` + fail-safe → Tasks 8, 9.
- Wiring + dispatch tests for bare/`-r`/`--no-pager` → Task 10.
- Doc updates (README, CLAUDE.md) → Task 11.

Placeholder scan: no `TBD`/`TODO` left in the plan; every code step shows code; every command shows the exact invocation and expected outcome.

Type/name consistency:

- Field is `Ascending bool` everywhere (Tasks 3, 4, 5, 7).
- Flag is `Reverse bool` with `short:"r"` (Tasks 3, 7, 9).
- New methods: `event.ListSeq`, `Store.ListSeq`, `eventStore.ListSeq`, `render.EventsStream`, `render.FlatStream`/`CSVStream`/`JSONStream`, `withPager`, `newPagerCmd`, `errPagerStartFailed`, `pagerCommand`, `noopCloser`, `formatLocalStamp`, `nowFunc`, `pinNow`, `staticSeq`, `errorAtSeq`, `toJSONEvent` — referenced consistently.

---

Plan complete and saved to `docs/superpowers/plans/2026-04-18-list-ux-overhaul.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
