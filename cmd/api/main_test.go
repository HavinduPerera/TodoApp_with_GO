package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

// TestServeShutsDown is the first test that needs a real socket: serve's job is
// the lifecycle of a listening server, which a recorder cannot exercise. Port 0
// asks the kernel for any free port; because the test binds before serve starts
// (the same order run uses), ln.Addr() knows which port that was.
func TestServeShutsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Logf("serving on %s", ln.Addr()) // visible with -v: the port the kernel chose
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel() // if the test fails early, this still lets serve return and the goroutine exit

	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(ctx, ln, newHandler(), slog.New(slog.DiscardHandler), 2*time.Second)
	}()

	// First prove the server is really serving: a request over the network gets
	// an answer. Without this the cancel check below could pass vacuously.
	res, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz: status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	// Cancelling the context is exactly what a SIGINT does in main.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serve returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return within 5s of cancel")
	}
}
