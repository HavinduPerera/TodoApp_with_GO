// Command api is the HTTP server for todoapp. This file is wiring and
// lifecycle only: it builds the pieces, connects them, and decides how the
// process starts and stops. Handlers live in internal/web, data in a store.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"todoapp/internal/config"
	"todoapp/internal/memstore"
	"todoapp/internal/todo"
	"todoapp/internal/web"
)

// version is set at build time by the linker:
//
//	go build -ldflags "-X main.version=<value>"
//
// The Makefile passes `git describe --always --dirty`. Under a plain `go run`
// or `go build` nothing sets it, and buildVersion falls back.
var version string

// main stays tiny: it owns the signal context and turns run's error into an
// exit code, nothing else. os.Exit skips deferred functions, so it must only
// ever be called here, where the only defer is stop() and nothing is left to
// clean up. run returns errors instead of calling log.Fatal for the same reason.
func main() {
	// NotifyContext hands us a context that is cancelled by the first SIGINT
	// (Ctrl-C) or SIGTERM (docker stop, kill). The signal stays registered until
	// stop() runs, so a second Ctrl-C during the drain is swallowed: how long we
	// wait is decided by the shutdown timeout, not by impatience.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.LookupEnv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run wires the application together and blocks until ctx is cancelled or the
// server fails. It takes its inputs as parameters instead of reaching for
// os.Getenv and os.Stderr itself, so a test can hand it a closure over a map
// and a bytes.Buffer.
func run(ctx context.Context, lookupEnv func(string) (string, bool), stderr io.Writer) error {
	// Configuration is read once, here, and every bad variable is reported in
	// this one error. Nothing below ever looks at the environment again.
	cfg, err := config.Load(lookupEnv)
	if err != nil {
		return err
	}
	logger := newLogger(stderr, cfg)
	slog.SetDefault(logger) // log.Print and friends (the log package's default logger) now come out structured too, at INFO
	ver := buildVersion()   // read once, like the config, and passed to whoever needs it

	// Dependency wiring reads bottom-up: the store knows nothing about HTTP, the
	// Service receives the store through the todo.Store interface, the server
	// receives the Service, main receives the server. Nothing reaches for a
	// package-level variable, so a test can build the same graph with a fresh store.
	store := memstore.New()
	svc := todo.NewService(store)
	if err := seedIfEmpty(ctx, svc); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	srv := web.NewServer(web.Deps{Service: svc, Logger: logger, Version: ver})

	// Bind here, synchronously, before anything runs in the background. A port
	// that is already taken is an immediate error with a clear message, and
	// because the listener exists before serve starts, ln.Addr() is the real
	// address: that is what lets a test listen on port 0 and ask which port it got.
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("server listening", "addr", ln.Addr().String(), "version", ver)

	return serve(ctx, ln, srv, logger, cfg.ShutdownTimeout)
}

// newLogger builds the one logger the process uses: text for a terminal, JSON
// for anything that collects logs, at the configured minimum level.
func newLogger(w io.Writer, cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// buildVersion reports what is running, from the most deliberate source to
// the least: the version the build explicitly linked in, then the VCS revision
// the go command records on its own when `go build` runs inside a git checkout
// (-buildvcs=auto), then "dev". `go run` leaves that automatic stamping off,
// and a Docker build context usually has no .git, so those two show "dev"
// unless -X is passed.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		var revision, modified string
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if revision != "" {
			if len(revision) > 12 {
				revision = revision[:12]
			}
			if modified == "true" {
				revision += "-dirty"
			}
			return revision
		}
	}
	return "dev"
}

// seedIfEmpty gives an empty store two example todos so a fresh `make run` has
// something to show. It goes through the Service like any client would, so the
// examples obey the same rules as real data. This is development convenience,
// not a product feature: milestone 8 puts it behind TODO_SEED once data persists.
func seedIfEmpty(ctx context.Context, svc *todo.Service) error {
	st, err := svc.Stats(ctx)
	if err != nil {
		return err
	}
	if st.Total > 0 {
		return nil
	}
	first, err := svc.Create(ctx, todo.CreateParams{Title: "Understand how a Go handler works"})
	if err != nil {
		return err
	}
	done := true
	if _, err := svc.Update(ctx, first.ID, todo.UpdateParams{Done: &done}); err != nil {
		return err
	}
	_, err = svc.Create(ctx, todo.CreateParams{Title: "Replace the in-memory store with SQLite"})
	return err
}

// serve runs an http.Server on ln until ctx is cancelled, then drains it and
// returns nil. It takes a listener rather than an address so the shutdown test
// controls the port, and it returns rather than exits so the test can observe
// the result.
func serve(ctx context.Context, ln net.Listener, handler http.Handler, logger *slog.Logger, shutdownTimeout time.Duration) error {
	srv := &http.Server{
		Handler: handler,
		// Each timeout defends against a different kind of slow or stuck client.
		ReadHeaderTimeout: 5 * time.Second,                                     // headers must arrive promptly: stops a client that trickles one byte at a time (slowloris)
		ReadTimeout:       10 * time.Second,                                    // the whole request, body included
		WriteTimeout:      30 * time.Second,                                    // the clock starts once the request headers are read, so a handler's own time counts against it; a slow handler is not stopped, its response just never reaches the client
		IdleTimeout:       120 * time.Second,                                   // a keep-alive connection waiting for its next request
		MaxHeaderBytes:    1 << 16,                                             // 64 KiB of request headers is plenty; the default is 1 MiB
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn), // the server's own complaints at WARN; without this they would still be structured (via SetDefault) but at INFO
		// BaseContext is deliberately NOT derived from ctx: that would cancel
		// every in-flight request's r.Context() the instant the signal arrives,
		// so handlers that honour it would abort mid-request, the opposite of
		// graceful. (Cancellation is advisory: it closes a Done channel, it does
		// not stop code.)
	}

	// Serve blocks until the server stops, so it runs in its own goroutine while
	// this one waits for whichever happens first: Serve failing, or ctx being
	// cancelled. The channel has capacity 1 so the goroutine can drop its result
	// in and exit whether or not anyone ever receives it. The normal path below
	// does receive (the <-errCh after Shutdown), but the early returns do not:
	// if Shutdown fails we return before that receive, and with an unbuffered
	// channel the Serve goroutine would be parked on its send forever, which is
	// exactly what a goroutine leak looks like. Rule of thumb: a goroutine that
	// sends must never depend on a receive that an error return can skip.
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	// select blocks until one of its cases can proceed, then runs that case
	// only. If both are ready at once Go picks one at random; either way the
	// real Serve error is what gets returned, because the <-errCh after
	// Shutdown below catches it too.
	select {
	case err := <-errCh:
		// Serve failed on its own (it never returns nil) and has already closed
		// the listener. Requests already in flight are abandoned: the process is
		// about to exit with an error anyway.
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		// ctx was cancelled: a stop signal in main, cancel() in the test. Leave
		// the select and drain below.
	}
	// NotifyContext records which signal did it; context.Cause reads it back.
	logger.Info("shutdown started", "cause", context.Cause(ctx), "timeout", shutdownTimeout)

	// Shutdown stops accepting, closes idle connections, and waits for in-flight
	// handlers to finish, up to the deadline of the context it is given. That
	// must be a FRESH context: ctx is already cancelled, and if any request is
	// still in flight, Shutdown with a cancelled context gives up on it at once
	// (returning context.Canceled) instead of waiting for it.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err) // context.DeadlineExceeded means handlers overran shutdownTimeout
	}

	// Shutdown closed the listener, which makes Serve return http.ErrServerClosed
	// into errCh. That is the success case here; anything else is a real failure.
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}
