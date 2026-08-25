package server

import (
	"net/http"
)

// healthResponse is the body of GET /health.
//
// Step 1 reports only that the process is up and serving. When the repository
// layer lands in Step 2 this grows a per-dependency "checks" object; the
// top-level "status" field keeps its meaning so probes written against 0.0.1
// continue to work.
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// handleHealth reports process liveness. It performs no I/O and touches no
// dependencies, so a slow or unreachable database cannot make the container
// look dead to Docker.
func (s *Server) handleHealth() http.HandlerFunc {
	body := healthResponse{Status: "ok", Version: s.version}
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, r, http.StatusOK, body)
	}
}
