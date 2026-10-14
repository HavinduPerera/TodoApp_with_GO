// Package todo holds the domain of the application: the Todo entity, the
// parameter and result types that cross package boundaries, and the errors
// other packages test for. It imports only the standard library, so every
// other package can import it without creating a cycle, and it knows nothing
// about HTTP or storage.
//
// It lives under internal/ so the go command enforces who may import it: only
// code rooted at the parent of the internal directory, which here means this
// module. An import from any other module fails with "use of internal package
// todoapp/internal/todo not allowed".
package todo

import "time"

// Todo is the domain entity. JSON tags live here because today the wire shape
// equals the domain shape; the day they diverge, a response type goes into the
// web package rather than bending the domain.
type Todo struct {
	ID        int64     `json:"id"` // int64 because SQLite's rowid is 64-bit; no narrowing casts anywhere
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"createdAt"` // UTC, truncated to microseconds
	UpdatedAt time.Time `json:"updatedAt"` // UTC; bumped on every mutation
}

// ListFilter narrows and pages a listing. A nil Done means "both".
type ListFilter struct {
	Done   *bool
	Limit  int
	Offset int
}

// Page is one page of a listing plus the total that ignores Limit and Offset.
// Items is never nil, so an empty page encodes as [] rather than null.
type Page struct {
	Items  []Todo `json:"items"`
	Total  int    `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// Stats is the answer to GET /api/todos/stats.
type Stats struct {
	Total int `json:"total"`
	Open  int `json:"open"`
	Done  int `json:"done"`
}

// DefaultLimit is the page size used when a caller does not ask for one.
const DefaultLimit = 50
