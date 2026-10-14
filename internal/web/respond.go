package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"todoapp/internal/todo"
)

// maxBodyBytes caps a request body. A todo is a few hundred bytes; 64 KiB
// leaves room to grow and stops a client from streaming gigabytes at the decoder.
const maxBodyBytes = 1 << 16

// handlerFunc is the shape of every API handler: it returns an error instead
// of writing one. Failing is `return err`, so a handler cannot forget the
// return after an error response (a classic bug with http.Error), and every
// status is written in exactly one place, writeError: transport failures
// arrive carrying theirs (apiError), domain errors are assigned theirs there.
// The rule that makes this work: a handler either writes the response or
// returns an error, never both; after writeJSON the only valid return is nil.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// handle adapts a handlerFunc to http.Handler by routing its error to writeError.
func (s *Server) handle(fn handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.writeError(w, r, err)
		}
	})
}

// apiError is a transport-level failure that already knows its status: a bad
// path parameter, an unsupported Content-Type, an unknown JSON field. Domain
// errors never carry statuses; writeError assigns them.
type apiError struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
}

func (e *apiError) Error() string { return e.Code + ": " + e.Message }

// errorEnvelope is the one shape every error response under /api/ uses. The
// request id joins it in milestone 9.
type errorEnvelope struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields,omitempty"`
	} `json:"error"`
}

// decodeJSON reads exactly one JSON object from the request body into v. It is
// strict on purpose: the Content-Type must be JSON (415), the body is capped
// (413), it must be an object (400), unknown keys are rejected (400) so a typo
// like "tittle" fails loudly instead of being silently ignored, and nothing may
// follow the first value (400). Errors it does not classify itself (syntax,
// wrong type, empty body, too large) are returned as they are; writeError knows
// each of them.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	// mime.ParseMediaType accepts "application/json; charset=utf-8", which a
	// plain string comparison would wrongly reject.
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return &apiError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type", Message: "Content-Type must be application/json"}
	}
	// Read the whole body, capped. Past the cap the read fails with
	// *http.MaxBytesError, and net/http marks the connection to close once the
	// 413 is written (a test's ResponseRecorder cannot do that part, and the
	// hint only reaches the server while w is its own writer, which milestone
	// 9's wrapper must keep in mind). 64 KiB is small enough to hold in memory,
	// which lets us look at the body before decoding it.
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return err // *http.MaxBytesError: writeError answers 413
	}
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return io.EOF // what a Decoder reports for an empty stream; writeError says "request body is empty"
	case raw[0] != '{':
		// null, a number, a string or an array are valid JSON but not a request:
		// encoding/json would leave v untouched for null and report a confusing
		// "wrong type" for the others, so say what is actually wrong.
		return &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "request body must be a JSON object"}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// The one string match in the app: encoding/json reports an unknown
		// field as a plain error value with no exported type, so the message
		// prefix is the only handle on it. (Since Go 1.25 encoding/json is a
		// compatibility layer over encoding/json/v2, which does have a typed
		// error; the layer flattens it back to this string on purpose, so the
		// match is stable.)
		if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
			if unquoted, err := strconv.Unquote(name); err == nil { // the name is %q-quoted: undo the Go escapes
				name = unquoted
			}
			return &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "unknown field",
				Fields: map[string]string{name: "unknown field"}}
		}
		return err // *json.SyntaxError, *json.UnmarshalTypeError, io.ErrUnexpectedEOF: writeError knows each
	}
	// A second Decode must hit EOF; anything else means a second value, or
	// garbage, after the first one.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &apiError{Status: http.StatusBadRequest, Code: "bad_request", Message: "request body must contain a single JSON value"}
	}
	return nil
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

// writeEnvelope writes an error envelope with the given status, code, message
// and optional per-field reasons.
func (s *Server) writeEnvelope(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	var env errorEnvelope
	env.Error.Code, env.Error.Message, env.Error.Fields = code, message, fields
	s.writeJSON(w, status, env)
}

// writeError is the single place an error becomes a status code. Each case is
// one kind of failure the app knows about; the default is "we did not expect
// this", which is logged in full and reported to the client as a bare 500.
// errors.As is for typed errors: it fills the pointer with the first value of
// that type in the chain (errors.AsType, Go 1.26+, is the generic form that
// returns it instead). errors.Is is for sentinels: it compares with == along
// the chain, or asks an Is method.
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	var ve *todo.ValidationError
	var mbe *http.MaxBytesError
	var se *json.SyntaxError
	var ute *json.UnmarshalTypeError
	switch {
	case errors.As(err, &ae):
		s.writeEnvelope(w, ae.Status, ae.Code, ae.Message, ae.Fields)
	case errors.As(err, &ve):
		s.writeEnvelope(w, http.StatusUnprocessableEntity, "validation_failed", ve.Error(), ve.Fields)
	case errors.Is(err, todo.ErrNotFound):
		s.writeEnvelope(w, http.StatusNotFound, "not_found", "todo not found", nil)
	case errors.As(err, &mbe):
		s.writeEnvelope(w, http.StatusRequestEntityTooLarge, "payload_too_large", fmt.Sprintf("body must be at most %d bytes", maxBodyBytes), nil)
	case errors.As(err, &se), errors.Is(err, io.ErrUnexpectedEOF):
		s.writeEnvelope(w, http.StatusBadRequest, "bad_request", "malformed JSON", nil) // never echo decoder text to the client
	case errors.As(err, &ute):
		if ute.Field == "" { // the whole body had the wrong shape; decodeJSON normally catches this earlier
			s.writeEnvelope(w, http.StatusBadRequest, "bad_request", "request body must be a JSON object", nil)
			return
		}
		s.writeEnvelope(w, http.StatusBadRequest, "bad_request", "wrong type for field", map[string]string{ute.Field: "wrong type"})
	case errors.Is(err, io.EOF):
		s.writeEnvelope(w, http.StatusBadRequest, "bad_request", "request body is empty", nil)
	case (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && r.Context().Err() != nil:
		// The client is gone; nobody would read a response. Log at debug and stop.
		s.logger.DebugContext(r.Context(), "client went away", "method", r.Method, "path", r.URL.Path)
	default:
		// Log the real error with enough context to find it; the client gets a
		// constant message and learns nothing about the internals.
		s.logger.ErrorContext(r.Context(), "unhandled error", "err", err, "method", r.Method, "path", r.URL.Path)
		s.writeEnvelope(w, http.StatusInternalServerError, "internal", "internal error", nil)
	}
}
