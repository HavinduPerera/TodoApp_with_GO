package web

import (
	"net/http"
	"strconv"

	"todoapp/internal/todo"
)

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request) {
	// Query parameters (done, limit, offset) are parsed in milestone 11; until
	// then every request gets the first page at the default size.
	f := todo.ListFilter{Limit: todo.DefaultLimit}
	items, total, err := s.store.List(r.Context(), f)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, todo.Page{Items: items, Total: total, Limit: f.Limit, Offset: f.Offset})
}

func (s *Server) getTodo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.writeEnvelope(w, http.StatusBadRequest, "bad_request", "id must be a positive integer")
		return
	}
	t, err := s.store.Get(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, t)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Stats(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

// pathID reads the {id} wildcard. A value that is not a positive integer is a
// malformed request (400), not a missing todo (404): "abc" and "-1" can never
// name a todo, whereas "99" is well formed and merely does not exist. ParseInt
// also accepts "+1" and "01"; they name the same todo as "1", which is fine for
// an API that never compares URLs as strings.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
