package todo

import (
	"errors"
	"maps"
	"slices"
	"strings"
)

// ErrNotFound is a sentinel error: one shared value that callers compare with
// errors.Is. It carries no payload, which is exactly when a sentinel is the
// right tool. Callers that wrap it with %w keep it findable: errors.Is walks
// the whole chain.
var ErrNotFound = errors.New("todo not found")

// ValidationError reports every field that failed validation at once, so a
// client can show all problems instead of discovering them one request at a
// time. It is a typed error, the other half of the pair: callers extract it
// with errors.As and read Fields, which a sentinel could never carry.
type ValidationError struct {
	Fields map[string]string // field name → reason a client can show
}

func (e *ValidationError) Error() string {
	keys := slices.Sorted(maps.Keys(e.Fields)) // map order is random; error text should not be
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return "validation failed: " + strings.Join(parts, "; ")
}
