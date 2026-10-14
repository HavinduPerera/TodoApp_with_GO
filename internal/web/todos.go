package web

import (
	"net/http"
	"strconv"

	"todoapp/internal/todo"
)

// Request bodies are transport shapes, separate from the domain types, so the
// wire format can change without the domain noticing.
type createTodoRequest struct {
	Title string `json:"title"`
}

// updateTodoRequest uses pointers to tell "absent" from "set to the zero
// value": {"done":false} must flip a todo back to open, while {} must leave
// done alone. JSON null also lands as nil, so null means absent.
type updateTodoRequest struct {
	Title *string `json:"title"`
	Done  *bool   `json:"done"`
}

func (s *Server) listTodos(w http.ResponseWriter, r *http.Request) error {
	// Query parameters arrive in milestone 11; the Service fills the default page size.
	page, err := s.svc.List(r.Context(), todo.ListFilter{})
	if err != nil {
		return err
	}
	s.writeJSON(w, http.StatusOK, page)
	return nil
}

func (s *Server) createTodo(w http.ResponseWriter, r *http.Request) error {
	var req createTodoRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	t, err := s.svc.Create(r.Context(), todo.CreateParams{Title: req.Title})
	if err != nil {
		return err
	}
	// 201 plus Location: the response says where the new resource now lives.
	w.Header().Set("Location", "/api/todos/"+strconv.FormatInt(t.ID, 10))
	s.writeJSON(w, http.StatusCreated, t)
	return nil
}

func (s *Server) getTodo(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	t, err := s.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	s.writeJSON(w, http.StatusOK, t)
	return nil
}

func (s *Server) updateTodo(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	var req updateTodoRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	t, err := s.svc.Update(r.Context(), id, todo.UpdateParams{Title: req.Title, Done: req.Done})
	if err != nil {
		return err
	}
	s.writeJSON(w, http.StatusOK, t)
	return nil
}

func (s *Server) deleteTodo(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	if err := s.svc.Delete(r.Context(), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent) // no body, so no Content-Type either
	return nil
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) error {
	st, err := s.svc.Stats(r.Context())
	if err != nil {
		return err
	}
	s.writeJSON(w, http.StatusOK, st)
	return nil
}

// pathID reads the {id} wildcard. A value that is not a positive integer is a
// malformed request (400), not a missing todo (404): "abc" and "-1" can never
// name a todo, whereas "99" is well formed and merely does not exist. ParseInt
// also accepts "+1" and "01"; they name the same todo as "1", which is fine for
// an API that never compares URLs as strings.
func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "id must be a positive integer"}
	}
	return id, nil
}
