package web

import (
	"context"
	"net/http"
	"time"
)

// healthz answers "is the process up". It deliberately touches nothing else,
// so a liveness probe never flaps because of a slow dependency: a database
// hiccup must not make an orchestrator restart a perfectly healthy process.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// readyz answers "can this process do useful work right now": shutdown has
// not begun, the pool's one connection is free, and SQLite answers a ping,
// all within a second. A request that keeps the connection for longer (a
// leaked Rows, a slow transaction) therefore shows up here as a 503. A 503
// tells a load balancer to route elsewhere; it never causes a restart, since
// restarts are decided by the liveness probe on /healthz. Both endpoints live
// outside /api/, so their bodies are small status objects rather than the
// API's error envelope.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "shutting down"})
		return
	}
	if s.checkReady != nil {
		// A bound of our own, inside the request's context: a probe must come
		// back quickly even when the database is wedged.
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := s.checkReady(ctx); err != nil {
			// A load balancer discards the body, so the operator learns why from the log.
			s.logger.WarnContext(r.Context(), "readiness check failed", "err", err)
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable", "reason": "db: " + err.Error()})
			return
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
