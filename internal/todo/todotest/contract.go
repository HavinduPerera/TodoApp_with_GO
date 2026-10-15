// Package todotest holds the conformance suite that every todo.Store must
// pass. Run it against each implementation (memstore now, SQLite from
// milestone 8): two stores that pass the same suite are interchangeable, and
// the suite is what lets the first one retire.
//
// It is a normal package, not a _test.go file, because test helpers in a
// _test.go file are invisible outside their own package. The standard library
// does the same with net/http/httptest, and testing/fstest.TestFS is this
// exact shape: one exported function any fs.FS implementation must pass.
// Nothing here reaches the binary: only test binaries import it (go list
// -deps ./cmd/api shows it absent from the binary's dependency graph).
package todotest

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"todoapp/internal/todo"
)

// base is a fixed instant so every expectation is an exact value. time.Date
// never carries a monotonic clock reading (only time.Now does), so what a
// store hands back prints and compares exactly as written here.
var base = time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)

// RunStoreContract runs every subtest against a fresh store from newStore,
// which must return an EMPTY store: the suite creates whatever it needs, so a
// failure always points at the method under test and never at a fixture.
func RunStoreContract(t *testing.T, newStore func(t *testing.T) todo.Store) {
	t.Helper()
	tests := []struct {
		name string
		run  func(t *testing.T, s todo.Store)
	}{
		{"create_assigns_id_and_keeps_fields", createAssignsIDAndKeepsFields},
		{"create_preserves_done", createPreservesDone},
		{"get_unknown_is_not_found", getUnknownIsNotFound},
		{"get_returns_a_copy", getReturnsACopy},
		{"list_empty_is_not_nil", listEmptyIsNotNil},
		{"list_newest_first_id_breaks_ties", listNewestFirstIDBreaksTies},
		{"list_same_second_keeps_order", listSameSecondKeepsOrder},
		{"list_filter_limit_offset_total", listFilterLimitOffsetTotal},
		{"list_past_the_end_is_empty_not_nil", listPastTheEndIsEmptyNotNil},
		{"list_returns_copies", listReturnsCopies},
		{"update_title_only_keeps_done", updateTitleOnlyKeepsDone},
		{"update_nil_fields_changes_only_updated_at", updateNilFieldsChangesOnlyUpdatedAt},
		{"update_unknown_is_not_found", updateUnknownIsNotFound},
		{"delete_twice_is_not_found", deleteTwiceIsNotFound},
		{"stats_empty_and_non_empty", statsEmptyAndNonEmpty},
		{"non_utc_time_round_trips_equal", nonUTCTimeRoundTripsEqual},
		{"concurrent_writers", concurrentWriters},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.run(t, newStore(t))
		})
	}
}

// create is the suite's one way of inserting a row. t.Helper makes a failure
// here point at the subtest that called it, not at this line.
func create(t *testing.T, s todo.Store, title string, done bool, at time.Time) todo.Todo {
	t.Helper()
	got, err := s.Create(t.Context(), todo.Todo{Title: title, Done: done, CreatedAt: at, UpdatedAt: at})
	if err != nil {
		t.Fatalf("Create(%q): %v", title, err)
	}
	return got
}

func get(t *testing.T, s todo.Store, id int64) todo.Todo {
	t.Helper()
	got, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	return got
}

func list(t *testing.T, s todo.Store, f todo.ListFilter) ([]todo.Todo, int) {
	t.Helper()
	items, total, err := s.List(t.Context(), f)
	if err != nil {
		t.Fatalf("List(%+v): %v", f, err)
	}
	return items, total
}

func ids(items []todo.Todo) []int64 {
	out := make([]int64, len(items))
	for i, t := range items {
		out[i] = t.ID
	}
	return out
}

// sameTodo compares field by field, using time.Equal for the timestamps: ==
// on a time.Time also compares the location and monotonic data, so two values
// for the same instant can be != and still be the same time.
func sameTodo(a, b todo.Todo) bool {
	return a.ID == b.ID && a.Title == b.Title && a.Done == b.Done &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

func createAssignsIDAndKeepsFields(t *testing.T, s todo.Store) {
	got := create(t, s, "Buy milk", false, base)
	if got.ID <= 0 {
		t.Errorf("ID = %d, want > 0", got.ID)
	}
	if got.Title != "Buy milk" || got.Done {
		t.Errorf("got = %+v, want the title and done as created", got)
	}
	if !got.CreatedAt.Equal(base) || !got.UpdatedAt.Equal(base) {
		t.Errorf("timestamps = %v / %v, want both %v", got.CreatedAt, got.UpdatedAt, base)
	}
	if again := get(t, s, got.ID); !sameTodo(again, got) {
		t.Errorf("Get = %+v, want what Create returned %+v", again, got)
	}
}

func createPreservesDone(t *testing.T, s todo.Store) {
	got := create(t, s, "Already done", true, base)
	if !got.Done {
		t.Error("Create returned Done = false for a todo created done")
	}
	if again := get(t, s, got.ID); !again.Done {
		t.Error("Get returned Done = false for a todo created done")
	}
}

func getUnknownIsNotFound(t *testing.T, s todo.Store) {
	_, err := s.Get(t.Context(), 12345)
	if !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("Get(unknown) error = %v, want todo.ErrNotFound", err)
	}
}

func getReturnsACopy(t *testing.T, s todo.Store) {
	created := create(t, s, "Original", false, base)
	got := get(t, s, created.ID)
	got.Title = "changed by the caller"
	if again := get(t, s, created.ID); again.Title != "Original" {
		t.Fatal("Get handed out an alias of the stored value; callers must receive copies")
	}
}

func listEmptyIsNotNil(t *testing.T, s todo.Store) {
	items, total := list(t, s, todo.ListFilter{Limit: 10})
	if items == nil {
		t.Fatal("items is nil; it must be an empty slice so JSON shows [] rather than null")
	}
	if len(items) != 0 || total != 0 {
		t.Fatalf("len(items) = %d, total = %d; want 0 and 0", len(items), total)
	}
}

// listNewestFirstIDBreaksTies creates rows OUT of time order on purpose: b is
// created second but is the oldest, so a store that sorted by id alone would
// return it before a. CreatedAt must be the primary key and id only the
// tiebreak for rows that share an instant.
func listNewestFirstIDBreaksTies(t *testing.T, s todo.Store) {
	a := create(t, s, "a", false, base.Add(time.Hour))
	b := create(t, s, "b", false, base)
	c := create(t, s, "c", false, base.Add(time.Hour)) // same instant as a: the higher id comes first
	items, _ := list(t, s, todo.ListFilter{Limit: 10})
	if got, want := ids(items), []int64{c.ID, a.ID, b.ID}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v (newest first by CreatedAt, id descending on ties)", got, want)
	}
}

// listSameSecondKeepsOrder pins sub-second precision. The rows are created
// newest first, so if a store collapsed them onto one second the id tiebreak
// would put the oldest first; and a store that sorted timestamps as text in a
// layout where "…00Z" sorts after "…00.5Z" would misorder them too.
func listSameSecondKeepsOrder(t *testing.T, s todo.Store) {
	c := create(t, s, "c", false, base.Add(999*time.Millisecond))
	b := create(t, s, "b", false, base.Add(300*time.Millisecond))
	a := create(t, s, "a", false, base)
	items, _ := list(t, s, todo.ListFilter{Limit: 10})
	if got, want := ids(items), []int64{c.ID, b.ID, a.ID}; !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v (sub-second timestamps must keep their order)", got, want)
	}
}

func listFilterLimitOffsetTotal(t *testing.T, s todo.Store) {
	// Five todos a minute apart; the first, third and fifth are done. Newest
	// first means the fifth created, then the fourth, down to the first.
	var all []todo.Todo
	for i := range 5 {
		all = append(all, create(t, s, fmt.Sprintf("t%d", i+1), i%2 == 0, base.Add(time.Duration(i)*time.Minute)))
	}
	done, open := true, false

	items, total := list(t, s, todo.ListFilter{Limit: 10})
	if total != 5 || len(items) != 5 {
		t.Fatalf("unfiltered: total = %d, len = %d; want 5 and 5", total, len(items))
	}
	items, total = list(t, s, todo.ListFilter{Done: &done, Limit: 10})
	if total != 3 || len(items) != 3 {
		t.Errorf("done=true: total = %d, len = %d; want 3 and 3", total, len(items))
	}
	for _, it := range items {
		if !it.Done {
			t.Errorf("done=true returned an open todo: %+v", it)
		}
	}
	items, total = list(t, s, todo.ListFilter{Done: &open, Limit: 10})
	if total != 2 || len(items) != 2 {
		t.Errorf("done=false: total = %d, len = %d; want 2 and 2", total, len(items))
	}
	items, total = list(t, s, todo.ListFilter{Limit: 2})
	if total != 5 || !slices.Equal(ids(items), []int64{all[4].ID, all[3].ID}) {
		t.Errorf("limit 2: total = %d, ids = %v; want total 5 (ignores paging) and the two newest", total, ids(items))
	}
	items, _ = list(t, s, todo.ListFilter{Limit: 2, Offset: 2})
	if !slices.Equal(ids(items), []int64{all[2].ID, all[1].ID}) {
		t.Errorf("limit 2 offset 2: ids = %v; want the third and fourth newest", ids(items))
	}
	items, _ = list(t, s, todo.ListFilter{Limit: 2, Offset: 4})
	if !slices.Equal(ids(items), []int64{all[0].ID}) {
		t.Errorf("limit 2 offset 4: ids = %v; want only the oldest", ids(items))
	}
}

func listPastTheEndIsEmptyNotNil(t *testing.T, s todo.Store) {
	create(t, s, "only one", false, base)
	items, total := list(t, s, todo.ListFilter{Limit: 10, Offset: 5})
	if items == nil {
		t.Fatal("items is nil past the end; it must be an empty slice")
	}
	if len(items) != 0 || total != 1 {
		t.Fatalf("len(items) = %d, total = %d; want 0 and 1", len(items), total)
	}
}

func listReturnsCopies(t *testing.T, s todo.Store) {
	create(t, s, "Original", false, base)
	items, _ := list(t, s, todo.ListFilter{Limit: 10})
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	items[0].Title = "changed by the caller"
	again, _ := list(t, s, todo.ListFilter{Limit: 10})
	if len(again) != 1 {
		t.Fatalf("len(again) = %d, want 1", len(again))
	}
	if again[0].Title != "Original" {
		t.Fatal("List handed out an alias of a stored value; callers must receive copies")
	}
}

func updateTitleOnlyKeepsDone(t *testing.T, s todo.Store) {
	created := create(t, s, "Old title", true, base)
	later := base.Add(time.Minute)
	title := "New title"
	got, err := s.Update(t.Context(), created.ID, todo.UpdateParams{Title: &title}, later)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Title != "New title" || !got.Done {
		t.Errorf("got = %+v, want the new title and done still true", got)
	}
	if !got.UpdatedAt.Equal(later) || !got.CreatedAt.Equal(base) {
		t.Errorf("timestamps = created %v / updated %v, want %v / %v", got.CreatedAt, got.UpdatedAt, base, later)
	}
	if again := get(t, s, created.ID); !sameTodo(again, got) {
		t.Errorf("Get after Update = %+v, want %+v", again, got)
	}
}

func updateNilFieldsChangesOnlyUpdatedAt(t *testing.T, s todo.Store) {
	created := create(t, s, "Unchanged", true, base)
	later := base.Add(time.Minute)
	got, err := s.Update(t.Context(), created.ID, todo.UpdateParams{}, later)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Title != created.Title || got.Done != created.Done || !got.CreatedAt.Equal(base) {
		t.Errorf("got = %+v, want everything but UpdatedAt unchanged from %+v", got, created)
	}
	if !got.UpdatedAt.Equal(later) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, later)
	}
}

func updateUnknownIsNotFound(t *testing.T, s todo.Store) {
	done := true
	_, err := s.Update(t.Context(), 999, todo.UpdateParams{Done: &done}, base)
	if !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("Update(unknown) error = %v, want todo.ErrNotFound", err)
	}
}

func deleteTwiceIsNotFound(t *testing.T, s todo.Store) {
	created := create(t, s, "Throw me away", false, base)
	if err := s.Delete(t.Context(), created.ID); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := s.Delete(t.Context(), created.ID); !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("second Delete error = %v, want todo.ErrNotFound", err)
	}
	if _, err := s.Get(t.Context(), created.ID); !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("Get after Delete error = %v, want todo.ErrNotFound", err)
	}
}

func statsEmptyAndNonEmpty(t *testing.T, s todo.Store) {
	st, err := s.Stats(t.Context())
	if err != nil {
		t.Fatalf("Stats on empty: %v", err)
	}
	if st != (todo.Stats{}) {
		t.Errorf("Stats on empty = %+v, want all zero", st)
	}
	create(t, s, "a", true, base)
	create(t, s, "b", false, base)
	create(t, s, "c", true, base)
	if st, err = s.Stats(t.Context()); err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st != (todo.Stats{Total: 3, Open: 1, Done: 2}) {
		t.Errorf("Stats = %+v, want total 3, open 1, done 2", st)
	}
}

// nonUTCTimeRoundTripsEqual feeds the store a time expressed in another zone.
// The same instant must come back. A store that persists text in a layout with
// no zone offset (the fixed-width UTC layout the SQLite store will use) must
// convert to UTC on the way in, or IST's wall-clock digits would be read back
// as if they were UTC and a different instant would return. Keeping the offset
// in the text would also round-trip, but then the text would no longer sort by
// instant.
func nonUTCTimeRoundTripsEqual(t *testing.T, s todo.Store) {
	ist := time.FixedZone("IST", 5*3600+30*60)
	created := create(t, s, "Zoned", false, base.In(ist))
	got := get(t, s, created.ID)
	if !got.CreatedAt.Equal(base) {
		t.Fatalf("CreatedAt = %v, want the same instant as %v", got.CreatedAt, base)
	}
}

// concurrentWriters is the subtest that gives the race detector something to
// race on. Twenty goroutines each create eleven todos, then read one back,
// mark one done, delete one, list and count, all at the same time, so every
// Store method runs concurrently with every other. The other subtests are
// sequential, so a store with a missing lock passes them and only this one
// can catch it. Under -race, expect WARNING: DATA RACE blocks pointing at the
// unsynchronised lines, and then the runtime's own "fatal error: concurrent
// map writes", which aborts the whole test binary before the subtest can
// report: the runtime's map check works even without -race, but only -race
// would catch a plain-integer race such as an unguarded id counter.
//
// Goroutines never call t.Fatal (that is only valid from the test's own
// goroutine); they report through a channel and the test judges afterwards.
func concurrentWriters(t *testing.T, s todo.Store) {
	const writers, perWriter = 20, 10
	ctx := t.Context()
	later := base.Add(time.Second)

	var wg sync.WaitGroup
	errs := make(chan error, writers) // each goroutine sends at most one error and then returns, so no sender can block
	for w := range writers {
		// wg.Go starts the goroutine and does the Add(1) and Done bookkeeping;
		// the explicit form, wg.Add(1) before `go` and defer wg.Done() inside,
		// is what older code shows and means the same thing.
		wg.Go(func() {
			mine := make([]todo.Todo, 0, perWriter+1)
			for i := range perWriter + 1 { // one extra, to be deleted below
				created, err := s.Create(ctx, todo.Todo{Title: fmt.Sprintf("w%d-%d", w, i), CreatedAt: base, UpdatedAt: base})
				if err != nil {
					errs <- fmt.Errorf("writer %d create %d: %w", w, i, err)
					return
				}
				mine = append(mine, created)
			}
			if _, err := s.Get(ctx, mine[0].ID); err != nil {
				errs <- fmt.Errorf("writer %d get: %w", w, err)
				return
			}
			done := true
			if _, err := s.Update(ctx, mine[0].ID, todo.UpdateParams{Done: &done}, later); err != nil {
				errs <- fmt.Errorf("writer %d update: %w", w, err)
				return
			}
			if err := s.Delete(ctx, mine[perWriter].ID); err != nil {
				errs <- fmt.Errorf("writer %d delete: %w", w, err)
				return
			}
			if _, _, err := s.List(ctx, todo.ListFilter{Limit: 5}); err != nil {
				errs <- fmt.Errorf("writer %d list: %w", w, err)
				return
			}
			if _, err := s.Stats(ctx); err != nil {
				errs <- fmt.Errorf("writer %d stats: %w", w, err)
			}
		})
	}
	wg.Wait() // returns only when every goroutine has finished: nothing is still running
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Each writer left perWriter todos behind (one was deleted) and marked one done.
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Total != writers*perWriter || st.Done != writers {
		t.Errorf("Stats = %+v, want total %d and done %d: a lost update means a missing lock", st, writers*perWriter, writers)
	}
	items, total := list(t, s, todo.ListFilter{Limit: writers * perWriter})
	if total != writers*perWriter || len(items) != writers*perWriter {
		t.Fatalf("total = %d, len = %d; want %d", total, len(items), writers*perWriter)
	}
	seen := make(map[int64]bool, len(items))
	for _, it := range items {
		if seen[it.ID] {
			t.Errorf("id %d was handed out twice", it.ID)
		}
		seen[it.ID] = true
	}
}
