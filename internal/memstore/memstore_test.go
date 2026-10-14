// Package memstore_test is an external test package: it imports memstore like
// any other caller would, so it can only use the exported API. Milestone 5
// replaces these first tests with the store conformance suite.
package memstore_test

import (
	"errors"
	"math"
	"testing"

	"todoapp/internal/memstore"
	"todoapp/internal/todo"
)

func TestGetUnknownIsNotFound(t *testing.T) {
	s := memstore.New()
	_, err := s.Get(t.Context(), 99)
	if !errors.Is(err, todo.ErrNotFound) {
		t.Fatalf("Get(99) error = %v, want todo.ErrNotFound", err)
	}
}

func TestGetReturnsACopy(t *testing.T) {
	s := memstore.New()
	got, err := s.Get(t.Context(), 1)
	if err != nil {
		t.Fatalf("Get(1): %v", err)
	}
	got.Title = "changed by the caller"

	again, _ := s.Get(t.Context(), 1)
	if again.Title == got.Title {
		t.Fatal("Get handed out an alias of the stored value; callers must receive copies")
	}
}

func TestListReturnsCopies(t *testing.T) {
	s := memstore.New()
	items, _, err := s.List(t.Context(), todo.ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	items[0].Title = "changed by the caller"

	again, _, _ := s.List(t.Context(), todo.ListFilter{Limit: 10})
	if again[0].Title == items[0].Title {
		t.Fatal("List handed out an alias of a stored value; callers must receive copies")
	}
}

func TestListHugeLimitDoesNotOverflow(t *testing.T) {
	s := memstore.New()
	// Offset 1 plus a limit near MaxInt would wrap negative if the store added
	// them before clamping; the slice expression would then panic.
	items, total, err := s.List(t.Context(), todo.ListFilter{Limit: math.MaxInt, Offset: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || total != 2 {
		t.Fatalf("len(items) = %d, total = %d; want 1 and 2", len(items), total)
	}
}

func TestListNewestFirstWithTotal(t *testing.T) {
	s := memstore.New()
	items, total, err := s.List(t.Context(), todo.ListFilter{Limit: todo.DefaultLimit})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total = %d, len(items) = %d; want 2 and 2", total, len(items))
	}
	// The seeds share a timestamp, so the id tiebreak decides: 2 before 1.
	if items[0].ID != 2 || items[1].ID != 1 {
		t.Fatalf("order = %d, %d; want 2, 1", items[0].ID, items[1].ID)
	}
}

func TestListPastTheEndIsEmptyNotNil(t *testing.T) {
	s := memstore.New()
	items, total, err := s.List(t.Context(), todo.ListFilter{Limit: 10, Offset: 5})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if items == nil {
		t.Fatal("items is nil; it must be an empty slice so JSON shows [] not null")
	}
	if len(items) != 0 || total != 2 {
		t.Fatalf("len(items) = %d, total = %d; want 0 and 2", len(items), total)
	}
}
