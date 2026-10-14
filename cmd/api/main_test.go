package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRoutes drives the router directly: no port, no goroutine, no network.
// httptest.NewRecorder returns an http.ResponseWriter that records what the
// handler wrote, so the test can inspect it afterwards.
func TestRoutes(t *testing.T) {
	h := newHandler()

	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{name: "todos", path: "/api/todos", wantStatus: http.StatusOK},
		{name: "count", path: "/api/todos/count", wantStatus: http.StatusOK},
		{name: "old demo endpoint is gone", path: "/api/healthStatuses", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("GET %s: status = %d, want %d; body: %s", tt.path, rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	newHandler().ServeHTTP(rec, req)

	// Result() gives the response as a client would see it; rec.Header() is the
	// live map a handler mutates, which is not the same thing after WriteHeader.
	res := rec.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got, want := res.Header.Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want %q", body.Status, "ok")
	}
}
