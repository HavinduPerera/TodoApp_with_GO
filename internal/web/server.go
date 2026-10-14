// Package web is the HTTP transport: routing, JSON encoding, and the mapping
// from errors to status codes. It depends on the todo package for shapes and
// errors and, for now, on the concrete memstore; milestone 4 replaces that
// with the todo.Service so this package stops knowing where data lives, and
// milestone 9 adds middleware.
package web

import (
	"log/slog"
	"net/http"

	"todoapp/internal/memstore"
)

// Deps is everything a Server needs from the outside. Passing dependencies in
// explicitly, instead of reaching for package-level variables, is what makes a
// Server constructible in a test with a fresh store and a silent logger.
type Deps struct {
	// Store is concrete for now. Milestone 4 swaps this field for a
	// *todo.Service (also concrete: there is no Service interface); the one
	// interface in the app, todo.Store, lands between the Service and its store.
	Store *memstore.Store
	// Logger nil falls back to slog.Default(). This is the one place the package
	// touches the global logger, resolved once here so no handler ever does.
	Logger *slog.Logger
}

// Server is the http.Handler for the whole app. Create it with NewServer: the
// zero value has a nil mux, so its ServeHTTP panics rather than answering 404.
type Server struct {
	store  *memstore.Store
	logger *slog.Logger
	mux    *http.ServeMux
}

// NewServer wires routes to handlers. It returns the concrete *Server rather
// than an http.Handler ("accept interfaces, return structs"): a caller that
// only needs an http.Handler already has one, and a caller that needs more can
// see what it holds.
func NewServer(d Deps) *Server {
	if d.Store == nil {
		panic("web: Deps.Store is nil") // a wiring mistake is a programmer error: fail at construction, not on the first request
	}
	s := &Server{store: d.Store, logger: d.Logger, mux: http.NewServeMux()}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	s.routes()
	return s
}

// ServeHTTP makes *Server an http.Handler. Milestone 9 wraps the mux in
// middleware here; for now it is a plain delegation.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}
