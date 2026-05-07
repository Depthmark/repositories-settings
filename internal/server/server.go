// Package server hosts the HTTP surface: webhook intake, /api/reconcile,
// /api/validate, /api/check, and ops endpoints (/healthz, /readyz, /metrics).
//
// Routes are registered on a stdlib net/http.ServeMux. Middleware order
// (outer-most first): trace ID, access log, metrics. Webhooks add HMAC
// verification. The /api routes accept either an HMAC-signed Bearer token
// or the same webhook secret as a Bearer for simplicity.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/metrics"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
	"github.com/prometheus/client_golang/prometheus"
)

// Deps bundles handler dependencies.
type Deps struct {
	Logger        *slog.Logger
	Client        *ghclient.Client
	Reconciler    *reconciler.Reconciler
	WebhookSecret []byte
	APIToken      string
	Registry      *prometheus.Registry
	Settings      func(ctx context.Context, repo config.Repo, ref string) (*config.Settings, error)
	// AppSlug is the GitHub App's URL slug (e.g. "repo-settings"); used
	// to recognise mentions in PR comments. Falls back to "repo-settings".
	AppSlug string
}

// Server wraps the http.Server plus a readiness flag for /readyz.
type Server struct {
	httpSrv *http.Server
	ready   *atomic.Bool
	logger  *slog.Logger
}

// New constructs a Server bound to addr.
func New(addr string, d Deps) *Server {
	mux := http.NewServeMux()
	ready := &atomic.Bool{}
	ready.Store(true)

	mux.HandleFunc("GET /healthz", healthHandler())
	mux.HandleFunc("GET /readyz", readyHandler(ready))
	if d.Registry != nil {
		mux.Handle("GET /metrics", metrics.Handler(d.Registry))
	}

	mux.Handle("POST /webhook", &webhookHandler{deps: d})
	mux.Handle("POST /api/reconcile", &reconcileHandler{deps: d})
	mux.Handle("POST /api/validate", &validateHandler{deps: d})
	mux.Handle("POST /api/check", &checkHandler{deps: d})

	wrapped := chain(
		traceMiddleware(),
		accessLogMiddleware(d.Logger),
	)(mux)

	return &Server{
		httpSrv: &http.Server{
			Addr:              addr,
			Handler:           wrapped,
			ReadHeaderTimeout: 5 * time.Second,
		},
		ready:  ready,
		logger: d.Logger,
	}
}

// ListenAndServe blocks until ctx is cancelled or the server errors.
func (s *Server) ListenAndServe(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.httpSrv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		s.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.httpSrv.Shutdown(shutdownCtx)
	}
}

// writeJSON is a tiny convenience used by handlers.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
