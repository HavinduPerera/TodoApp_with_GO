package web

import "net/http"

// routes is the one place every pattern is registered, so the API surface can
// be read top to bottom. Patterns are "METHOD /path" (Go 1.22+); {id} is a
// wildcard read back with r.PathValue.
//
// Both "GET /api/todos/stats" and "GET /api/todos/{id}" match a request for
// /api/todos/stats. ServeMux resolves that by specificity, not by registration
// order: the literal segment is more specific than the wildcard, so stats wins.
// ServeMux's real rule is about requests, not segments: a pattern wins when
// every request it matches is also matched by the other and the other matches
// more (a strict subset). Two patterns that are each literal where the other
// is a wildcard are neither more specific, and registering both panics.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /readyz", s.readyz)

	s.mux.Handle("GET /api/todos", s.handle(s.listTodos))
	s.mux.Handle("POST /api/todos", s.handle(s.createTodo))
	s.mux.Handle("GET /api/todos/stats", s.handle(s.stats))
	s.mux.Handle("GET /api/todos/{id}", s.handle(s.getTodo))
	s.mux.Handle("PATCH /api/todos/{id}", s.handle(s.updateTodo))
	s.mux.Handle("DELETE /api/todos/{id}", s.handle(s.deleteTodo))

	// ServeMux's own 404 and 405 bodies are text/plain. Under /api/ every
	// response must be the envelope, so method-less patterns sit beside the
	// method-specific ones. A request with a known path but the wrong method
	// falls to these (a method-specific pattern is more specific, so it still
	// wins whenever it matches), and "/api/" with its trailing slash is a
	// subtree pattern that catches every unknown path below it.
	//
	// There is deliberately no method-less "/api/todos/stats": it would
	// conflict with "GET /api/todos/{id}" (each matches GET /api/todos/stats,
	// neither matches a strict subset of the other's requests) and ServeMux
	// panics at registration. PATCH and DELETE need explicit stats patterns
	// instead, because their {id} patterns would otherwise capture "stats" and
	// answer 400; a method-specific literal is a strict subset of the
	// method-specific wildcard, so these do not conflict. Every other method
	// reaches the method-less {id} fallback, which reports Allow for stats.
	allowGet := func(*http.Request) string { return "GET, HEAD" }
	s.mux.Handle("PATCH /api/todos/stats", s.methodNotAllowed(allowGet))
	s.mux.Handle("DELETE /api/todos/stats", s.methodNotAllowed(allowGet))
	s.mux.Handle("/api/todos", s.methodNotAllowed(func(*http.Request) string { return "GET, HEAD, POST" }))
	s.mux.Handle("/api/todos/{id}", s.methodNotAllowed(func(r *http.Request) string {
		if r.PathValue("id") == "stats" {
			return allowGet(r)
		}
		return "GET, HEAD, PATCH, DELETE"
	}))
	s.mux.Handle("/api/", s.handle(func(w http.ResponseWriter, r *http.Request) error {
		return &apiError{Status: http.StatusNotFound, Code: "not_found", Message: "no such endpoint"}
	}))
}

// methodNotAllowed answers 405 as JSON, with the Allow header a 405 must carry
// so the client learns which methods would have worked. A GET pattern also
// serves HEAD, so HEAD is listed wherever GET is, as ServeMux's own 405 does.
func (s *Server) methodNotAllowed(allow func(*http.Request) string) http.Handler {
	return s.handle(func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Allow", allow(r))
		return &apiError{Status: http.StatusMethodNotAllowed, Code: "method_not_allowed", Message: r.Method + " is not allowed here"}
	})
}
