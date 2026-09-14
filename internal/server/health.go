package server

import (
	"net/http"
	"sync/atomic"
)

// healthHandler answers GET /healthz with a constant 200 — proves the
// process is alive.
func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// readyHandler answers GET /readyz. It returns 503 once shutdown has
// flipped the readiness flag so a load balancer can drain in flight.
func readyHandler(ready *atomic.Bool) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if !ready.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
