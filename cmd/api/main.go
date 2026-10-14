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
	"syscall"
	"time"

	"todoapp/internal/memstore"
	"todoapp/internal/web"
)

// defaultAddr is loopback on purpose: there is no authentication yet, so the
// server should not be reachable from the rest of the network by default.
const defaultAddr = "127.0.0.1:8080"

// shutdownTimeout is how long in-flight requests get to finish after a stop
// signal before the process gives up on them. It becomes TODO_SHUTDOWN_TIMEOUT
// in milestone 6.
const shutdownTimeout = 10 * time.Second

// main stays tiny: it owns the signal context and turns run's error into an
// exit code, nothing else. os.Exit skips deferred functions, so it must only
// ever be called here, where the only defer is stop() and nothing is left to
// clean up. run returns errors instead of calling log.Fatal for the same reason.
func main() {
	// NotifyContext hands us a context that is cancelled by the first SIGINT
	// (Ctrl-C) or SIGTERM (docker stop, kill). The signal stays registered until
	// stop() runs, so a second Ctrl-C during the drain is swallowed: how long we
	// wait is decided by shutdownTimeout, not by impatience.
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
	logger := slog.New(slog.NewTextHandler(stderr, nil))
	slog.SetDefault(logger) // log.Print and friends (the log package's default logger) now come out structured too, at INFO

	addr, _ := lookupEnv("TODO_ADDR") // unset and set-but-empty both mean "use the default":
	if addr == "" {                   // net.Listen("tcp", "") would bind a random port on EVERY interface, the opposite of the loopback default
		addr = defaultAddr
	}

	// Dependency wiring reads bottom-up: the store knows nothing about HTTP, the
	// server receives the store, main receives the server. Nothing reaches for a
	// package-level variable, so a test can build the same graph with a fresh store.
	store := memstore.New()
	srv := web.NewServer(web.Deps{Store: store, Logger: logger})

	// Bind here, synchronously, before anything runs in the background. A port
	// that is already taken is an immediate error with a clear message, and
	// because the listener exists before serve starts, ln.Addr() is the real
	// address: that is what lets a test listen on port 0 and ask which port it got.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("server listening", "addr", ln.Addr().String())

	return serve(ctx, ln, srv, logger, shutdownTimeout)
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
