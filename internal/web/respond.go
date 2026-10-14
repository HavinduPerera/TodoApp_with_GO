package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"todoapp/internal/todo"
)

// errorEnvelope is the one shape every error response under /api/ uses. Fields
// for validation messages and the request id join it in milestones 4 and 9.
type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeJSON is the one place a response body is encoded. Marshalling before
// WriteHeader means a value that cannot be encoded becomes a clean 500 instead
// of a 200 whose body never arrives.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// A programming error, not a request error: log it with the value's type
		// and answer with a hand-written envelope so even this path keeps the shape.
		s.logger.Error("encode response", "err", err, "type", fmt.Sprintf("%T", v))
		status, body = http.StatusInternalServerError, []byte(`{"error":{"code":"internal","message":"internal error"}}`)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body))) // the payoff of marshalling first: net/http only infers this for bodies up to 2 KiB
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		s.logger.Debug("write response", "err", err) // almost always: the client went away
	}
}

// writeEnvelope writes an error envelope with the given status, code and message.
func (s *Server) writeEnvelope(w http.ResponseWriter, status int, code, message string) {
	var env errorEnvelope
	env.Error.Code, env.Error.Message = code, message
	s.writeJSON(w, status, env)
}

// writeError is the single place an error becomes a status code. Handlers do
// not decide statuses; they hand the error here. Milestone 4 grows this into
// the full switch (validation 422, decoding 400/413/415) and milestone 9 adds
// the request id to the envelope.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, todo.ErrNotFound):
		s.writeEnvelope(w, http.StatusNotFound, "not_found", "todo not found")
	default:
		// Log the real error with enough context to find it; the client gets a
		// constant message and learns nothing about the internals.
		s.logger.ErrorContext(r.Context(), "unhandled error", "err", err, "method", r.Method, "path", r.URL.Path)
		s.writeEnvelope(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
