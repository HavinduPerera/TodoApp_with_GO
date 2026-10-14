package web

import "net/http"

// healthz answers "is the process up". It deliberately touches nothing else,
// so a liveness probe never flaps because of a slow dependency. A readiness
// endpoint that does check the database arrives with SQLite in milestone 7.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
