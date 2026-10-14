// Package memstore is an in-memory todo store guarded by a mutex. It is the
// first store the app gets and it is deliberately simple: a map, a counter
// and a lock. It satisfies todo.Store without saying so: Go interfaces are
// implicit, so having the methods is enough. Milestone 8 retires it once the
// SQLite store passes the same tests.
//
// The package is named for what it is, a short noun, not for the layer it
// sits in: Go packages are "memstore" and "sqlite", never "models" or "utils".
package memstore

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"todoapp/internal/todo"
)

// Store keeps todos in a map. Use it only through the pointer New returns: the
// struct holds a sync.RWMutex and a map, and copying the struct copies the
// mutex (its state, not a reference) but only the map header, so the copy
// shares the map with the original while locking a different lock. Two writers
// could then race on one map, each believing it holds the lock. Pointer
// receivers on every method make the intent explicit, and go vet's copylocks
// check catches most accidental copies.
type Store struct {
	mu     sync.RWMutex
	todos  map[int64]todo.Todo // values, not pointers: a copy handed out can never alias the map's entry
	nextID int64
}

// New returns a Store seeded with the two todos the hardcoded slice used to
// hold, so this milestone changes the structure of the app, not its answers.
func New() *Store {
	s := &Store{todos: make(map[int64]todo.Todo)}
	// UTC so the JSON form carries Z rather than this machine's offset; UTC()
	// also drops the monotonic clock reading, so == holds after a JSON round
	// trip. Truncate to microseconds matches the precision the SQLite TEXT
	// column will store from milestone 8: JSON itself keeps nanoseconds, so the
	// truncation is for the future store, not for the wire.
	now := time.Now().UTC().Truncate(time.Microsecond)
	s.insert(todo.Todo{Title: "Understand how a Go handler works", Done: true, CreatedAt: now, UpdatedAt: now})
	s.insert(todo.Todo{Title: "Replace this hardcoded slice", Done: false, CreatedAt: now, UpdatedAt: now})
	return s
}

// insert assigns the next id and stores t. Callers must hold mu for writing;
// New does not need to, because nothing else can see the Store yet.
func (s *Store) insert(t todo.Todo) todo.Todo {
	s.nextID++
	t.ID = s.nextID
	s.todos[t.ID] = t
	return t
}

// Create assigns the next id and stores t. It takes the write lock: a reader
// must not observe the map mid-insert, and two Creates must never hand out the
// same id. (Milestone 5 removes this lock on purpose to watch the race
// detector catch it.)
func (s *Store) Create(ctx context.Context, t todo.Todo) (todo.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.insert(t), nil
}

// Get returns the todo with the given id, or todo.ErrNotFound.
func (s *Store) Get(ctx context.Context, id int64) (todo.Todo, error) {
	s.mu.RLock()         // any number of readers may hold the read lock at once; a writer waits for all of them
	defer s.mu.RUnlock() // defer runs on every return path, so an early return can never leak the lock

	t, ok := s.todos[id]
	if !ok {
		return todo.Todo{}, todo.ErrNotFound
	}
	return t, nil // t is a copy of the map entry: the caller may change it without touching the store
}

// List returns the page described by f, newest first (CreatedAt descending,
// then ID descending so equal timestamps still have a stable order), plus the
// number of todos that match the filter before paging. Limit and Offset are
// applied as given, except that negative values count as zero and an Offset
// past the end yields an empty page, so the slice expression can never panic.
// The store applies what it is given: the Service supplies the default page
// size, and the transport rejects out-of-range values (milestone 11).
func (s *Store) List(ctx context.Context, f todo.ListFilter) ([]todo.Todo, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// make([]T, 0, n) rather than var items []T: a nil slice encodes to JSON
	// as null, an empty one as [], and the API promises [].
	matching := make([]todo.Todo, 0, len(s.todos))
	for _, t := range s.todos { // map iteration order is random on purpose, hence the sort below
		if f.Done != nil && t.Done != *f.Done {
			continue
		}
		matching = append(matching, t)
	}
	slices.SortFunc(matching, func(a, b todo.Todo) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 { // b before a: descending
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})

	total := len(matching)
	start := min(max(f.Offset, 0), total)
	end := start + min(max(f.Limit, 0), total-start) // bound the limit by what remains BEFORE adding, so the sum cannot overflow
	return matching[start:end], total, nil
}

// Update applies the non-nil fields of p and sets UpdatedAt under one write
// lock, so two concurrent PATCHes cannot interleave their read-modify-write.
func (s *Store) Update(ctx context.Context, id int64, p todo.UpdateParams, updatedAt time.Time) (todo.Todo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	t, ok := s.todos[id]
	if !ok {
		return todo.Todo{}, todo.ErrNotFound
	}
	if p.Title != nil {
		t.Title = *p.Title
	}
	if p.Done != nil {
		t.Done = *p.Done
	}
	t.UpdatedAt = updatedAt
	s.todos[id] = t // t is a copy of the entry: write it back or the change is lost
	return t, nil
}

// Delete removes the todo or reports ErrNotFound, so a second DELETE of the
// same id is visibly a mistake rather than a silent no-op.
func (s *Store) Delete(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.todos[id]; !ok {
		return todo.ErrNotFound
	}
	delete(s.todos, id)
	return nil
}

// Stats counts todos in one pass under the read lock.
func (s *Store) Stats(ctx context.Context) (todo.Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var st todo.Stats
	for _, t := range s.todos {
		st.Total++
		if t.Done {
			st.Done++
		} else {
			st.Open++
		}
	}
	return st, nil
}
