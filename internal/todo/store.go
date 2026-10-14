package todo

import (
	"context"
	"time"
)

// Store is what the Service needs from persistence, and nothing more. It is
// declared here, next to its consumer, rather than next to an implementation:
// Go interfaces are satisfied implicitly, so a type in another package becomes
// a Store simply by having these methods, and the package that needs the
// behaviour gets to say exactly which methods those are.
//
// It also has to be an interface for a mechanical reason. Service needs a
// field type for its store, and todo cannot import memstore because memstore
// already imports todo; an import cycle is a compile error in Go. So the field
// type must be something todo can see without importing a store: an interface.
// Declaring it here, next to its consumer, is also the better place for it.
//
// Values, not pointers, cross this boundary: Todo is small, and a store that
// returns copies can never hand out aliased state. Every method takes a
// context first, so a store that does I/O can stop when the request is gone.
type Store interface {
	// Create stores every field of t except ID, which it assigns, and returns
	// the stored todo with ID set.
	Create(ctx context.Context, t Todo) (Todo, error)
	// Get returns ErrNotFound for an unknown id.
	Get(ctx context.Context, id int64) (Todo, error)
	// List returns one page, newest first, plus the total that ignores paging.
	// items is never nil.
	List(ctx context.Context, f ListFilter) (items []Todo, total int, err error)
	// Update applies the non-nil fields of p and sets UpdatedAt as one atomic
	// step; ErrNotFound for an unknown id.
	Update(ctx context.Context, id int64, p UpdateParams, updatedAt time.Time) (Todo, error)
	// Delete returns ErrNotFound when nothing was deleted.
	Delete(ctx context.Context, id int64) error
	Stats(ctx context.Context) (Stats, error)
}
