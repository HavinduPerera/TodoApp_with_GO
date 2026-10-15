// Package web is the HTTP transport: routing, JSON decoding and encoding, and
// the mapping from errors to status codes. It depends on the todo package for
// shapes, errors and the Service, and on nothing that knows where data lives.
// Milestone 9 adds middleware here.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"

	"todoapp/internal/todo"
)

// Deps is everything a Server needs from the outside. Passing dependencies in
// explicitly, instead of reaching for package-level variables, is what makes a
// Server constructible in a test with a fresh store and a silent logger.
type Deps struct {
	// Service applies the domain rules; handlers never touch a store directly.
	// It is concrete on purpose: there is no Service interface, so handler
	// tests run the real rules over a real (in-memory) store. The one interface
	// in the app, todo.Store, sits between the Service and its store.
	Service *todo.Service
	// Logger nil falls back to slog.Default(). This is the one place the package
	// touches the global logger, resolved once here so no handler ever does.
	Logger *slog.Logger
	// Version is reported by /healthz so an operator can see what is running.
	Version string
	// Ready checks the dependencies /readyz must vouch for, today the database
	// (db.PingContext fits the signature). nil means there is nothing to check.
	Ready func(context.Context) error
}

// Server is the http.Handler for the whole app. Create it with NewServer: the
// zero value has a nil mux, so its ServeHTTP panics rather than answering 404.
type Server struct {
	svc        *todo.Service
	logger     *slog.Logger
	version    string
	checkReady func(context.Context) error
	ready      atomic.Bool // flipped to false by SetReady when shutdown begins; read by /readyz on every request
	mux        *http.ServeMux
}

// NewServer wires routes to handlers. It returns the concrete *Server rather
// than an http.Handler, the "return structs" half of the community rule
// "accept interfaces, return structs" (NewService shows the other half): a
// caller that only needs an http.Handler already has one, and a caller that
// needs more can see what it holds.
func NewServer(d Deps) *Server {
	if d.Service == nil {
		panic("web: Deps.Service is nil") // a wiring mistake is a programmer error: fail at construction, not on the first request
	}
	s := &Server{svc: d.Service, logger: d.Logger, version: d.Version, checkReady: d.Ready, mux: http.NewServeMux()}
	if s.logger == nil {
		s.logger = slog.Default()
	}
	s.ready.Store(true)
	s.routes()
	return s
}

// SetReady tells /readyz whether to vouch for this process. serve() calls
// SetReady(false) before http.Server.Shutdown, so a load balancer that polls
// /readyz stops sending new requests here while the ones in flight finish.
// atomic.Bool because the HTTP handlers read it from their own goroutines
// while the shutdown goroutine writes it: a plain bool would be a data race.
func (s *Server) SetReady(ok bool) {
	s.ready.Store(ok)
}

// ServeHTTP makes *Server an http.Handler. Milestone 9 wraps the mux in
// middleware here; for now it is a plain delegation.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}
