// Package web_test is an external test package: it can only use what web
// exports, which proves the exported surface is enough to drive the server.
package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"todoapp/internal/memstore"
	"todoapp/internal/todo"
	"todoapp/internal/web"
)

var fixed = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

const jsonCT = "application/json"

// newServer builds a Server over the real Service and a fresh store holding
// the two example todos (id 1 done, id 2 open, like a fresh `make run`), with a
// silent logger and a pinned clock. The Service is returned too so a test can
// move the clock between requests. Every test gets its own server, which is
// what makes t.Parallel safe below.
func newServer(t *testing.T) (http.Handler, *todo.Service) {
	t.Helper()
	svc := todo.NewService(memstore.New())
	svc.Now = func() time.Time { return fixed }
	first, err := svc.Create(t.Context(), todo.CreateParams{Title: "Understand how a Go handler works"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	done := true
	if _, err := svc.Update(t.Context(), first.ID, todo.UpdateParams{Done: &done}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := svc.Create(t.Context(), todo.CreateParams{Title: "Replace the in-memory store with SQLite"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return web.NewServer(web.Deps{Service: svc, Logger: slog.New(slog.DiscardHandler), Version: "test"}), svc
}

// do drives the handler directly, no socket, and returns the response as a
// client would see it. t.Helper makes failures point at the caller's line.
func do(t *testing.T, h http.Handler, method, path, contentType, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	return do(t, h, http.MethodGet, path, "", "")
}

// decode reads a JSON body into v and fails the test if it cannot.
func decode(t *testing.T, res *http.Response, v any) {
	t.Helper()
	if got, want := res.Header.Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

// Test-local wire types spell out the documented JSON names on purpose:
// decoding into the server's own structs would hide a mistyped tag, because
// the same tag would be used on both sides.
type envelope struct {
	Error struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields"`
	} `json:"error"`
}

type wireTodo struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func TestHealthz(t *testing.T) {
	t.Parallel() // independent server per test: nothing shared, so tests may overlap; handlers then really run concurrently, which is the only way -race can catch shared state inside web
	h, _ := newServer(t)
	res := get(t, h, "/healthz")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	var body struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	decode(t, res, &body)
	if body.Status != "ok" || body.Version != "test" {
		t.Errorf("body = %+v, want status ok and the Version NewServer was given", body)
	}
}

func TestReadyz(t *testing.T) {
	t.Parallel()
	newWith := func(ready func(context.Context) error) *web.Server {
		return web.NewServer(web.Deps{Service: todo.NewService(memstore.New()), Logger: slog.New(slog.DiscardHandler), Ready: ready})
	}
	type status struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	t.Run("ok when the check passes", func(t *testing.T) {
		t.Parallel()
		res := get(t, newWith(func(context.Context) error { return nil }), "/readyz")
		var body status
		decode(t, res, &body)
		if res.StatusCode != http.StatusOK || body.Status != "ok" {
			t.Errorf("status = %d, body = %+v; want 200 and ok", res.StatusCode, body)
		}
	})
	t.Run("503 with the reason when the check fails", func(t *testing.T) {
		t.Parallel()
		res := get(t, newWith(func(context.Context) error { return errors.New("database is locked") }), "/readyz")
		var body status
		decode(t, res, &body)
		if res.StatusCode != http.StatusServiceUnavailable || body.Reason != "db: database is locked" {
			t.Errorf("status = %d, body = %+v; want 503 and the db reason", res.StatusCode, body)
		}
	})
	t.Run("503 while shutting down, without calling the check", func(t *testing.T) {
		t.Parallel()
		srv := newWith(func(context.Context) error { t.Error("check must not run once SetReady(false) was called"); return nil })
		srv.SetReady(false)
		res := get(t, srv, "/readyz")
		var body status
		decode(t, res, &body)
		if res.StatusCode != http.StatusServiceUnavailable || body.Reason != "shutting down" {
			t.Errorf("status = %d, body = %+v; want 503 and shutting down", res.StatusCode, body)
		}
	})
	t.Run("check gets a context that expires", func(t *testing.T) {
		t.Parallel()
		res := get(t, newWith(func(ctx context.Context) error {
			if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > time.Second {
				return fmt.Errorf("readiness deadline = %v, want one within 1s", dl)
			}
			return nil
		}), "/readyz")
		if res.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200: the check must receive a context with a deadline", res.StatusCode)
		}
	})
}

func TestReadRoutes(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t)
	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantCode   string // error envelope code; empty means "do not check the body"
	}{
		{name: "list", path: "/api/todos", wantStatus: http.StatusOK},
		{name: "get existing", path: "/api/todos/2", wantStatus: http.StatusOK},
		{name: "get unknown is a JSON 404", path: "/api/todos/99", wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "non-integer id is a 400", path: "/api/todos/abc", wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "zero id is a 400", path: "/api/todos/0", wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "negative id is a 400", path: "/api/todos/-1", wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "stats beats the {id} wildcard", path: "/api/todos/stats", wantStatus: http.StatusOK},
		{name: "count is gone, so it parses as a bad id", path: "/api/todos/count", wantStatus: http.StatusBadRequest, wantCode: "bad_request"},
		{name: "unknown api path is a JSON 404", path: "/api/healthStatuses", wantStatus: http.StatusNotFound, wantCode: "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // read-only rows against one server: safe to overlap
			res := get(t, h, tt.path)
			if res.StatusCode != tt.wantStatus {
				t.Fatalf("GET %s: status = %d, want %d", tt.path, res.StatusCode, tt.wantStatus)
			}
			if tt.wantCode == "" {
				return
			}
			var env envelope
			decode(t, res, &env)
			if env.Error.Code != tt.wantCode {
				t.Errorf("GET %s: error.code = %q, want %q", tt.path, env.Error.Code, tt.wantCode)
			}
		})
	}
}

// TestRequestErrors has one row per way a write request can be wrong. Each row
// is a contract line from the API table in ARCHITECTURE.md §5.
func TestRequestErrors(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t)
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		wantStatus  int
		wantCode    string
		wantField   string // a key that must be present in error.fields
		wantAllow   string // the Allow header a 405 must carry
	}{
		{name: "blank title", method: "POST", path: "/api/todos", contentType: jsonCT, body: `{"title":"   "}`, wantStatus: 422, wantCode: "validation_failed", wantField: "title"},
		{name: "title too long", method: "POST", path: "/api/todos", contentType: jsonCT, body: `{"title":"` + strings.Repeat("a", 201) + `"}`, wantStatus: 422, wantCode: "validation_failed", wantField: "title"},
		{name: "unknown field names the typo", method: "POST", path: "/api/todos", contentType: jsonCT, body: `{"tittle":"x"}`, wantStatus: 400, wantCode: "bad_request", wantField: "tittle"},
		{name: "wrong content type", method: "POST", path: "/api/todos", contentType: "text/plain", body: `{"title":"x"}`, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "missing content type", method: "POST", path: "/api/todos", contentType: "", body: `{"title":"x"}`, wantStatus: 415, wantCode: "unsupported_media_type"},
		{name: "malformed json", method: "POST", path: "/api/todos", contentType: jsonCT, body: `nope`, wantStatus: 400, wantCode: "bad_request"},
		{name: "wrong type names the field", method: "POST", path: "/api/todos", contentType: jsonCT, body: `{"title":5}`, wantStatus: 400, wantCode: "bad_request", wantField: "title"},
		{name: "empty body", method: "POST", path: "/api/todos", contentType: jsonCT, body: ``, wantStatus: 400, wantCode: "bad_request"},
		{name: "truncated json", method: "POST", path: "/api/todos", contentType: jsonCT, body: `{"title":"x"`, wantStatus: 400, wantCode: "bad_request"},
		{name: "two values", method: "POST", path: "/api/todos", contentType: jsonCT, body: `{"title":"a"} {"title":"b"}`, wantStatus: 400, wantCode: "bad_request"},
		{name: "whitespace only is an empty body", method: "POST", path: "/api/todos", contentType: jsonCT, body: "  \n ", wantStatus: 400, wantCode: "bad_request"},
		{name: "json null is not an object", method: "POST", path: "/api/todos", contentType: jsonCT, body: `null`, wantStatus: 400, wantCode: "bad_request"},
		{name: "json array is not an object", method: "POST", path: "/api/todos", contentType: jsonCT, body: `[1]`, wantStatus: 400, wantCode: "bad_request"},
		{name: "json string is not an object", method: "PATCH", path: "/api/todos/1", contentType: jsonCT, body: `"done"`, wantStatus: 400, wantCode: "bad_request"},
		{name: "body too large", method: "POST", path: "/api/todos", contentType: jsonCT, body: `{"title":"` + strings.Repeat("x", 70_000) + `"}`, wantStatus: 413, wantCode: "payload_too_large"},
		{name: "patch with nothing to change", method: "PATCH", path: "/api/todos/1", contentType: jsonCT, body: `{}`, wantStatus: 422, wantCode: "validation_failed", wantField: "body"},
		{name: "patch with null counts as nothing", method: "PATCH", path: "/api/todos/1", contentType: jsonCT, body: `{"done":null}`, wantStatus: 422, wantCode: "validation_failed", wantField: "body"},
		{name: "patch with a blank title", method: "PATCH", path: "/api/todos/1", contentType: jsonCT, body: `{"title":" "}`, wantStatus: 422, wantCode: "validation_failed", wantField: "title"},
		{name: "patch unknown id", method: "PATCH", path: "/api/todos/99", contentType: jsonCT, body: `{"done":true}`, wantStatus: 404, wantCode: "not_found"},
		{name: "patch bad id", method: "PATCH", path: "/api/todos/abc", contentType: jsonCT, body: `{"done":true}`, wantStatus: 400, wantCode: "bad_request"},
		{name: "delete unknown id", method: "DELETE", path: "/api/todos/99", wantStatus: 404, wantCode: "not_found"},
		{name: "put is not allowed", method: "PUT", path: "/api/todos/1", contentType: jsonCT, body: `{}`, wantStatus: 405, wantCode: "method_not_allowed", wantAllow: "GET, HEAD, PATCH, DELETE"},
		{name: "delete on the collection is not allowed", method: "DELETE", path: "/api/todos", wantStatus: 405, wantCode: "method_not_allowed", wantAllow: "GET, HEAD, POST"},
		{name: "post to stats is not allowed", method: "POST", path: "/api/todos/stats", contentType: jsonCT, body: `{}`, wantStatus: 405, wantCode: "method_not_allowed", wantAllow: "GET, HEAD"},
		{name: "patch to stats is not allowed", method: "PATCH", path: "/api/todos/stats", contentType: jsonCT, body: `{"done":true}`, wantStatus: 405, wantCode: "method_not_allowed", wantAllow: "GET, HEAD"},
		{name: "delete of stats is not allowed", method: "DELETE", path: "/api/todos/stats", wantStatus: 405, wantCode: "method_not_allowed", wantAllow: "GET, HEAD"},
		{name: "unknown api path with a body is a JSON 404", method: "POST", path: "/api/nope", contentType: jsonCT, body: `{}`, wantStatus: 404, wantCode: "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // every row is rejected before it can change anything, so rows may overlap
			res := do(t, h, tt.method, tt.path, tt.contentType, tt.body)
			if res.StatusCode != tt.wantStatus {
				b, _ := io.ReadAll(res.Body)
				t.Fatalf("%s %s: status = %d, want %d; body: %s", tt.method, tt.path, res.StatusCode, tt.wantStatus, b)
			}
			var env envelope
			decode(t, res, &env)
			if env.Error.Code != tt.wantCode {
				t.Errorf("error.code = %q, want %q", env.Error.Code, tt.wantCode)
			}
			if tt.wantField != "" && env.Error.Fields[tt.wantField] == "" {
				t.Errorf("error.fields = %v, want a reason under %q", env.Error.Fields, tt.wantField)
			}
			if tt.wantAllow != "" && res.Header.Get("Allow") != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", res.Header.Get("Allow"), tt.wantAllow)
			}
		})
	}
}

func TestCreatePatchDeleteFlow(t *testing.T) {
	t.Parallel()
	h, svc := newServer(t)

	// Create: 201, Location, body is the new todo with both timestamps = now.
	res := do(t, h, http.MethodPost, "/api/todos", jsonCT+"; charset=utf-8", `{"title":"  Buy milk  "}`)
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("POST status = %d, want 201; body: %s", res.StatusCode, b)
	}
	var created wireTodo
	decode(t, res, &created)
	if created.ID <= 0 || created.Title != "Buy milk" || created.Done {
		t.Fatalf("created = %+v, want a positive id, trimmed title, done false", created)
	}
	// Build the paths from the id the server chose, not from a seed count: the
	// fixture changes in milestone 8 and this flow must not care.
	path := "/api/todos/" + strconv.FormatInt(created.ID, 10)
	if got := res.Header.Get("Location"); got != path {
		t.Errorf("Location = %q, want %q", got, path)
	}
	if !created.CreatedAt.Equal(fixed) || !created.UpdatedAt.Equal(fixed) {
		t.Errorf("timestamps = %v / %v, want %v", created.CreatedAt, created.UpdatedAt, fixed)
	}

	// Patch a minute later: done flips, updatedAt moves, createdAt and title stay.
	later := fixed.Add(time.Minute)
	svc.Now = func() time.Time { return later }
	res = do(t, h, http.MethodPatch, path, jsonCT, `{"done":true}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", res.StatusCode)
	}
	var patched wireTodo
	decode(t, res, &patched)
	if !patched.Done || patched.Title != "Buy milk" {
		t.Errorf("patched = %+v, want done true and the same title", patched)
	}
	if !patched.UpdatedAt.Equal(later) || !patched.CreatedAt.Equal(fixed) {
		t.Errorf("timestamps = created %v / updated %v, want %v / %v", patched.CreatedAt, patched.UpdatedAt, fixed, later)
	}

	// A GET shows the same state.
	var fetched wireTodo
	decode(t, get(t, h, path), &fetched)
	if !fetched.Done {
		t.Error("GET after PATCH: done = false, want true")
	}

	// Delete: 204 with no body; a second delete and a GET are 404.
	res = do(t, h, http.MethodDelete, path, "", "")
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", res.StatusCode)
	}
	if b, _ := io.ReadAll(res.Body); len(b) != 0 {
		t.Errorf("DELETE body = %q, want empty", b)
	}
	if res = do(t, h, http.MethodDelete, path, "", ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("second DELETE status = %d, want 404", res.StatusCode)
	}
	if res = get(t, h, path); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE status = %d, want 404", res.StatusCode)
	}
}

// TestEmptyListIsAnEmptyArray drives an unseeded server: the API promises []
// for an empty list, never null, and only a raw look at the bytes can tell the
// two apart (a []wireTodo decodes both to nil).
func TestEmptyListIsAnEmptyArray(t *testing.T) {
	t.Parallel()
	h := web.NewServer(web.Deps{Service: todo.NewService(memstore.New()), Logger: slog.New(slog.DiscardHandler)})
	res := get(t, h, "/api/todos")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	var page struct {
		Items json.RawMessage `json:"items"`
		Total int             `json:"total"`
	}
	decode(t, res, &page)
	if string(page.Items) != "[]" || page.Total != 0 {
		t.Errorf("items = %s, total = %d; want [] and 0", page.Items, page.Total)
	}
}

func TestListEnvelope(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t)
	res := get(t, h, "/api/todos")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	var page struct {
		Items  []wireTodo `json:"items"`
		Total  int        `json:"total"`
		Limit  int        `json:"limit"`
		Offset int        `json:"offset"`
	}
	decode(t, res, &page)
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("total = %d, len(items) = %d; want 2 and 2", page.Total, len(page.Items))
	}
	if page.Limit != todo.DefaultLimit || page.Offset != 0 {
		t.Errorf("limit = %d, offset = %d; want %d and 0", page.Limit, page.Offset, todo.DefaultLimit)
	}
	if page.Items[0].ID != 2 {
		t.Errorf("first item id = %d, want 2 (newest first)", page.Items[0].ID)
	}
	if page.Items[0].Title == "" || page.Items[0].CreatedAt.IsZero() || page.Items[0].UpdatedAt.IsZero() {
		t.Errorf("item is missing fields: %+v", page.Items[0])
	}
}

func TestStats(t *testing.T) {
	t.Parallel()
	h, _ := newServer(t)
	res := get(t, h, "/api/todos/stats")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	var st struct {
		Total int `json:"total"`
		Open  int `json:"open"`
		Done  int `json:"done"`
	}
	decode(t, res, &st)
	if st.Total != 2 || st.Open != 1 || st.Done != 1 {
		t.Errorf("stats = %+v, want total 2, open 1, done 1", st)
	}
}
