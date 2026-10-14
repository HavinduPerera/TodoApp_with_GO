package todo

import "errors"

// ErrNotFound is a sentinel error: one shared value that callers compare with
// errors.Is. It carries no payload, which is exactly when a sentinel is the
// right tool; a typed error with per-field messages arrives in milestone 4 for
// validation. Callers that wrap it with %w keep it findable: errors.Is walks
// the whole chain.
var ErrNotFound = errors.New("todo not found")
