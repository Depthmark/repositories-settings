package ghclient

import (
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/metrics"
)

// The transport chain, outermost first:
//
//	rateLimitTransport   — waits for budget, in priority order
//	conditionalGet       — If-None-Match, serves cached bodies on 304
//	authTransport        — mints and attaches the installation token
//	metricsTransport     — records the true wire outcome
//	base
//
// Everything the service needs from a GitHub call lives here rather than
// in a call wrapper, so an SDK that builds its own requests still gets
// rate limiting, caching, auth and metrics for free.

// rateLimitTransport runs each request through the two-pool scheduler
// and feeds the response's x-ratelimit headers back into it.
type rateLimitTransport struct {
	next http.RoundTripper
	rl   *RateLimiter
}

func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.rl == nil {
		return t.next.RoundTrip(req)
	}
	ci := callFrom(req.Context())
	return Schedule(req.Context(), t.rl, ci.prio, ci.pool, func() (*http.Response, error) {
		resp, err := t.next.RoundTrip(req)
		if resp != nil {
			t.rl.UpdateFromResponse(ci.pool, resp.Header)
		}
		return resp, err
	})
}

// authTransport attaches an installation token scoped to the owner named
// in the call context. A request that already carries an Authorization
// header is left alone, so a caller can still present its own credential.
type authTransport struct {
	next http.RoundTripper
	auth *AppAuth
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ci := callFrom(req.Context())
	if t.auth == nil || ci.owner == "" || req.Header.Get("Authorization") != "" {
		return t.next.RoundTrip(req)
	}
	instID, err := t.auth.InstallationID(req.Context(), ci.owner)
	if err != nil {
		return nil, fmt.Errorf("resolving installation for %q: %w", ci.owner, err)
	}
	tok, err := t.auth.InstallationToken(req.Context(), instID)
	if err != nil {
		return nil, fmt.Errorf("minting installation token for %q: %w", ci.owner, err)
	}
	// RoundTrippers must not mutate the request they are given.
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+tok)
	return t.next.RoundTrip(clone)
}

// metricsTransport records the outcome of the actual network exchange.
// It sits below the conditional-GET layer so a 304 is visible as a 304;
// the cache's own hit/miss counter reports what that 304 saved.
type metricsTransport struct {
	next http.RoundTripper
}

func (t *metricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ci := callFrom(req.Context())
	resp, err := t.next.RoundTrip(req)
	status := "error"
	if resp != nil {
		status = fmt.Sprintf("%d", resp.StatusCode/100*100)
	}
	metrics.APICallsTotal.WithLabelValues(req.Method, ci.route, status).Inc()
	return resp, err
}
