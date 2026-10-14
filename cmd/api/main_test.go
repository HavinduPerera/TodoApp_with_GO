package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"todoapp/internal/memstore"
	"todoapp/internal/web"
)

// TestServeShutsDown needs a real socket: serve's job is the lifecycle of a
// listening server, which a recorder cannot exercise. Port 0 asks the kernel
// for any free port; because the test binds before serve starts (the same
// order run uses), ln.Addr() knows which port that was. Handler-level tests
// moved to internal/web with the handlers.
func TestServeShutsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Logf("serving on %s", ln.Addr()) // visible with -v: the port the kernel chose
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel() // if the test fails early, this still lets serve return and the goroutine exit

	logger := slog.New(slog.DiscardHandler)
	handler := web.NewServer(web.Deps{Store: memstore.New(), Logger: logger})
	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(ctx, ln, handler, logger, 2*time.Second)
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
