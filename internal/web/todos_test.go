// Package web_test is an external test package: it can only use what web
// exports, which proves the exported surface is enough to drive the server.
package web_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"todoapp/internal/memstore"
	"todoapp/internal/todo"
	"todoapp/internal/web"
)

// newServer builds a Server over a fresh seeded store with a silent logger.
func newServer(t *testing.T) http.Handler {
	t.Helper()
	return web.NewServer(web.Deps{Store: memstore.New(), Logger: slog.New(slog.DiscardHandler)})
}

// get drives the handler directly, no socket, and returns the response as a
// client would see it. t.Helper makes failures point at the caller's line.
func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	t.Cleanup(func() { res.Body.Close() })
	return res
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

type envelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func TestHealthz(t *testing.T) {
	res := get(t, newServer(t), "/healthz")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	var body struct {
		Status string `json:"status"`
	}
	decode(t, res, &body)
	if body.Status != "ok" {
		t.Errorf("status = %q, want %q", body.Status, "ok")
	}
}

func TestRoutes(t *testing.T) {
	h := newServer(t)
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
		{name: "old demo endpoint is gone", path: "/api/healthStatuses", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

func TestListEnvelope(t *testing.T) {
	res := get(t, newServer(t), "/api/todos")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	// Decode into a test-local struct that spells out the documented wire names,
	// not into todo.Page: decoding into the producer's own type would hide a
	// mistyped json tag, because the same tag would be used on both sides.
	var page struct {
		Items []struct {
			ID        int64  `json:"id"`
			Title     string `json:"title"`
			Done      bool   `json:"done"`
			CreatedAt string `json:"createdAt"`
			UpdatedAt string `json:"updatedAt"`
		} `json:"items"`
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
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
	if page.Items[0].Title == "" || page.Items[0].CreatedAt == "" || page.Items[0].UpdatedAt == "" {
		t.Errorf("item is missing fields: %+v", page.Items[0])
	}
}

func TestStats(t *testing.T) {
	res := get(t, newServer(t), "/api/todos/stats")
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
