// Package memstore_test runs the store conformance suite against memstore.
// The suite lives in todo/todotest so the SQLite store can run the very same
// subtests in milestone 8; when both pass, the two are interchangeable.
package memstore_test

import (
	"math"
	"testing"

	"todoapp/internal/memstore"
	"todoapp/internal/todo"
	"todoapp/internal/todo/todotest"
)

func TestStore(t *testing.T) {
	todotest.RunStoreContract(t, func(t *testing.T) todo.Store {
		return memstore.New()
	})
}

// TestListHugeLimitDoesNotOverflow is memstore-specific, not part of the
// contract: a Limit this large is outside what the transport ever lets through
// (1..200), and a SQL store that pre-allocates its result slice from Limit
// could not honour it. Here it guards the slice arithmetic: Offset 1 plus a
// limit near MaxInt would wrap negative if the store added them before
// clamping, and the slice expression would then panic.
func TestListHugeLimitDoesNotOverflow(t *testing.T) {
	s := memstore.New()
	for _, title := range []string{"a", "b"} {
		if _, err := s.Create(t.Context(), todo.Todo{Title: title}); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	items, total, err := s.List(t.Context(), todo.ListFilter{Limit: math.MaxInt, Offset: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || total != 2 {
		t.Fatalf("len(items) = %d, total = %d; want 1 and 2", len(items), total)
	}
}
