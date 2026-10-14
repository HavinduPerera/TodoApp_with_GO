package web

// routes is the one place every pattern is registered, so the API surface can
// be read top to bottom. Patterns are "METHOD /path" (Go 1.22+); {id} is a
// wildcard read back with r.PathValue.
//
// Both "GET /api/todos/stats" and "GET /api/todos/{id}" match a request for
// /api/todos/stats. ServeMux resolves that by specificity, not by registration
// order: the literal segment is more specific than the wildcard, so stats wins.
// ServeMux's real rule is about requests, not segments: a pattern wins when
// every request it matches is also matched by the other. Two patterns that are
// each literal where the other is a wildcard are neither more specific, and
// registering both panics.
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /api/todos", s.listTodos)
	s.mux.HandleFunc("GET /api/todos/stats", s.stats)
	s.mux.HandleFunc("GET /api/todos/{id}", s.getTodo)
}
