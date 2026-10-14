// Command api is the HTTP server for todoapp.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Todo is the shape GET /api/todos returns. The data is the hardcoded slice
// below until milestone 3 moves it behind an in-memory store (SQLite replaces
// that in milestone 8).
type Todo struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	CreatedAt time.Time `json:"createdAt"`
}

var todos = []Todo{
	{ID: 1, Title: "Understand how a Go handler works", Done: true, CreatedAt: time.Now()},
	{ID: 2, Title: "Replace this hardcoded slice", Done: false, CreatedAt: time.Now()},
}

// defaultAddr is loopback on purpose: there is no authentication yet, so the
// server should not be reachable from the rest of the network by default.
const defaultAddr = "127.0.0.1:8080"

// main stays tiny: it turns run's error into an exit code and nothing else.
// os.Exit skips deferred functions, so it must only ever be called here, where
// nothing is left to clean up. run returns errors instead of calling log.Fatal
// for the same reason.
func main() {
	if err := run(os.LookupEnv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run wires the server together and blocks until it stops. It takes its inputs
// as parameters instead of reaching for os.Getenv and os.Stderr itself, so a
// test can hand it a closure over a map and a bytes.Buffer.
func run(lookupEnv func(string) (string, bool), stderr io.Writer) error {
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	slog.SetDefault(logger) // log.Print and friends (the log package's default logger) now come out structured too, at INFO

	addr, _ := lookupEnv("TODO_ADDR") // unset and set-but-empty both mean "use the default":
	if addr == "" {                   // an empty http.Server.Addr would bind ":http", port 80
		addr = defaultAddr
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: newHandler(),
		// Each timeout defends against a different kind of slow or stuck client.
		ReadHeaderTimeout: 5 * time.Second,                                     // headers must arrive promptly: stops a client that trickles one byte at a time (slowloris)
		ReadTimeout:       10 * time.Second,                                    // the whole request, body included
		WriteTimeout:      30 * time.Second,                                    // the clock starts once the request headers are read, so a handler's own time counts against it; a slow handler is not stopped, its response just never reaches the client
		IdleTimeout:       120 * time.Second,                                   // a keep-alive connection waiting for its next request
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn), // the server's own complaints at WARN; without this they would still be structured (via SetDefault) but at INFO
	}

	// Logged before the bind, so it is a statement of intent, not of fact;
	// milestone 2 listens first and then reports the address actually bound.
	logger.Info("starting server", "addr", addr)

	// ListenAndServe never returns nil (go doc: "always returns a non-nil
	// error"), so there is no success path to return from here yet. Milestone 2
	// adds one: http.ErrServerClosed after a graceful Shutdown is the success case.
	return fmt.Errorf("serve: %w", srv.ListenAndServe())
}

// newHandler builds the router. Keeping it separate from run lets a test call
// its ServeHTTP through httptest without opening a socket.
func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /api/todos", listTodos)
	mux.HandleFunc("GET /api/todos/count", countTodos)
	return mux
}

// healthz answers "is the process up". It deliberately touches nothing else,
// so a liveness probe never flaps because of a slow dependency.
func healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func listTodos(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, todos)
}

func countTodos(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"count": len(todos)})
}

// writeJSON is the one place a response body is encoded. Marshalling before
// WriteHeader means a value that cannot be encoded becomes a clean 500 instead
// of a 200 whose body never arrives. In milestone 3 this helper moves to the
// web package, gets an injected logger, and gains a JSON error envelope.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode response", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.Debug("write response", "err", err) // almost always: the client went away
	}
}
