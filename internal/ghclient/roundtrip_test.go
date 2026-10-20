package ghclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/logger"
)

// The transport chain is what lets go-github inherit rate limiting,
// caching, auth and metrics without knowing they exist. Each layer reads
// its instructions from the call context, so the tests below check that
// the annotation actually reaches them.

func TestCallContext_DefaultsAreSafe(t *testing.T) {
	ci := callFrom(context.Background())
	if ci.route != RouteOther {
		t.Errorf("an un-annotated call must be bucketed as %q, not labelled with its URL; got %q", RouteOther, ci.route)
	}
	if ci.pool != PoolREST {
		t.Errorf("pool should default to REST, got %q", ci.pool)
	}
	if ci.prio != PriorityCronReconcile {
		t.Errorf("an un-annotated call should take the lowest priority, got %v", ci.prio)
	}
}

func TestCallContext_CarriesAnnotation(t *testing.T) {
	ctx := WithCall(context.Background(), PriorityPRCheck, "acme", "/repos/{owner}/{repo}/hooks")
	ci := callFrom(ctx)
	if ci.prio != PriorityPRCheck || ci.owner != "acme" || ci.pool != PoolREST {
		t.Fatalf("annotation lost: %+v", ci)
	}
	if got := RouteFromContext(ctx); got != "/repos/{owner}/{repo}/hooks" {
		t.Fatalf("route lost: %q", got)
	}
}

func TestCallContext_GraphQLUsesItsOwnPool(t *testing.T) {
	ci := callFrom(WithGraphQLCall(context.Background(), PriorityPRCheck, "acme"))
	if ci.pool != PoolGraphQL {
		t.Fatalf("GraphQL calls must draw on the GraphQL budget, got %q", ci.pool)
	}
	if ci.route != "/graphql" {
		t.Fatalf("unexpected route %q", ci.route)
	}
}

// A request that already carries a credential is left alone, and one
// without an owner cannot have a token minted for it.
func TestAuthTransport_LeavesAnnotatedRequestsAlone(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	tr := &authTransport{auth: nil, next: srv.Client().Transport}
	req, _ := http.NewRequestWithContext(
		WithCall(context.Background(), PriorityPRCheck, "acme", "/x"),
		http.MethodGet, srv.URL, nil)
	req.Header.Set("Authorization", "Bearer caller-supplied")
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if seen != "Bearer caller-supplied" {
		t.Fatalf("caller credential should survive, got %q", seen)
	}
}

// The rate limiter is in the chain, so every request — including ones
// go-github builds — passes through the scheduler.
func TestRateLimitTransport_SchedulesEveryRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4321")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rl := NewRateLimiter(logger.Discard(), Options{Concurrency: 2})
	defer rl.Stop()
	tr := &rateLimitTransport{rl: rl, next: srv.Client().Transport}

	req, _ := http.NewRequestWithContext(
		WithCall(context.Background(), PriorityPRCheck, "acme", "/x"),
		http.MethodGet, srv.URL, nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	// The response headers must feed back into the pool's budget view,
	// or the limiter paces against a number that never moves.
	if remaining, _ := rl.Snapshot(PoolREST); remaining != 4321 {
		t.Fatalf("rate-limit headers not fed back: remaining = %d", remaining)
	}
}

// go-github must resolve paths against the configured API root, the same
// value the raw client concatenates. Two notions of the base URL in one
// process is a debugging problem nobody should inherit.
func TestGitHubClient_UsesConfiguredBaseURL(t *testing.T) {
	for _, apiURL := range []string{
		"https://ghes.example.com/api/v3",
		"https://ghes.example.com/api/v3/",
	} {
		gh := newGitHubClient(http.DefaultClient, apiURL)
		if got := gh.BaseURL.String(); got != "https://ghes.example.com/api/v3/" {
			t.Errorf("APIURL %q → BaseURL %q, want the API root with a trailing slash", apiURL, got)
		}
	}
}
