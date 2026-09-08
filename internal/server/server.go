// Package server hosts the HTTP surface: webhook intake, /api/reconcile,
// /api/validate, /api/check, and ops endpoints (/healthz, /readyz, /metrics).
//
// Routes are registered on a stdlib net/http.ServeMux. Middleware order
// (outer-most first): trace ID, access log, metrics. Webhooks add HMAC
// verification. The privileged /api routes require a Bearer credential:
// either a GitHub Actions OIDC token, which binds the caller to its own
// repository, or the static operator token, which does not.
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
	"github.com/Depthmark/repositories-settings/internal/oidc"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
	"github.com/prometheus/client_golang/prometheus"
)

// Request-hardening limits. The webhook body cap matches GitHub's own
// payload ceiling; the API cap is deliberately tighter because
// /api/validate is unauthenticated and parses whatever it is handed.
//
// WriteTimeout is generous because reconciles still run inline on the
// request goroutine; it exists to bound a stuck connection, not to
// police handler latency. It can drop sharply once intake is async.
const (
	MaxWebhookBody = 5 << 20 // 5 MiB
	MaxAPIBody     = 1 << 20 // 1 MiB
	MaxHeaderBytes = 1 << 20 // 1 MiB
	ReadTimeout    = 30 * time.Second
	WriteTimeout   = 5 * time.Minute
	IdleTimeout    = 90 * time.Second
)

// Deps bundles handler dependencies.
type Deps struct {
	Logger        *slog.Logger
	Client        *ghclient.Client
	Reconciler    *reconciler.Reconciler
	WebhookSecret []byte
	APIToken      string
	// OIDC verifies GitHub Actions workflow tokens on the privileged API
	// routes. Nil disables the mode, leaving the static APIToken as the
	// only credential. When set, a caller is bound to the repository its
	// token names; see internal/server/auth.go.
	OIDC *oidc.Verifier
	// AllowUnauthenticated is an explicit development-only escape hatch.
	// Production defaults fail closed when either ingress credential is empty.
	AllowUnauthenticated bool
	Registry             *prometheus.Registry
	// Settings resolves a repository's desired state: the org layer, any
	// suborg tiers, the repository's own files, and the policy verdict on
	// the result. Handlers hand the whole resolution to the reconciler so
	// policy is enforced on every trigger, not per call site.
	Settings func(ctx context.Context, repo config.Repo, ref string) (*config.Resolution, error)
	// AppSlug is the GitHub App's URL slug (e.g. "repo-settings"); used
	// to recognise mentions in PR comments. Falls back to "repo-settings".
	AppSlug string
	// DisabledResources is the operator-level deny-set surfaced both
	// to /api/validate (warns when YAML configures a disabled section)
	// and used to populate the Reconciler's deny-set at boot.
	DisabledResources config.DisabledResources
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

	mux.Handle("POST /webhook", limitBody(MaxWebhookBody, &webhookHandler{deps: d}))
	mux.Handle("POST /api/reconcile", limitBody(MaxAPIBody, &reconcileHandler{deps: d}))
	mux.Handle("POST /api/validate", limitBody(MaxAPIBody, &validateHandler{deps: d}))
	mux.Handle("POST /api/check", limitBody(MaxAPIBody, &checkHandler{deps: d}))

	wrapped := chain(
		traceMiddleware(),
		accessLogMiddleware(d.Logger),
	)(mux)

	return &Server{
		httpSrv: &http.Server{
			Addr:              addr,
			Handler:           wrapped,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       ReadTimeout,
			WriteTimeout:      WriteTimeout,
			IdleTimeout:       IdleTimeout,
			MaxHeaderBytes:    MaxHeaderBytes,
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
