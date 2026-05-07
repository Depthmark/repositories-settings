package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

// makeRepos returns n test repos under a single owner so prefetch
// chunks share an installation.
func makeRepos(n int) []config.Repo {
	out := make([]config.Repo, n)
	for i := 0; i < n; i++ {
		out[i] = config.Repo{Owner: "o", Name: repoName(i)}
	}
	return out
}

func repoName(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "r0"
	}
	out := []byte{}
	for i > 0 {
		out = append([]byte{digits[i%10]}, out...)
		i /= 10
	}
	return "r" + string(out)
}

// fakeGraphQLServer returns a server that decodes the graphql aliases in
// the request body and responds with one node per alias, plus tracks the
// number of /graphql calls.
func fakeGraphQLServer(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&calls, 1)
		var req struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// We don't parse aliases out of the query; reply with an empty
		// data map. The worker treats missing aliases as "no prefetch
		// for this repo" and falls through to its non-prefetch path.
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	return srv, &calls
}

func newTestClient(srv *httptest.Server) (*ghclient.Client, *ghclient.RateLimiter) {
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 5})
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
	return cl, rl
}

// 120 repos, BatchSize=50 → 3 batches → 3 prefetch GraphQL calls.
// Each repo has nil settings so the reconciler does no further API work.
func TestReconcileBatch_PartitionsAndPrefetchesPerBatch(t *testing.T) {
	srv, calls := fakeGraphQLServer(t)
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	rec := reconciler.New(logger.Discard())
	w := New(rec, cl, logger.Discard())
	w.BatchSize = 50
	w.Concurrency = 8

	repos := makeRepos(120)
	settings := func(_ config.Repo) (*config.Settings, error) { return &config.Settings{}, nil }
	reports := w.ReconcileBatch(context.Background(), repos, settings, true)

	if len(reports) != 120 {
		t.Fatalf("got %d reports, want 120", len(reports))
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Fatalf("graphql prefetch calls = %d, want 3 (120/50 ceil)", got)
	}
}

// Empty input is a no-op, returning a nil slice.
func TestReconcileBatch_EmptyInput(t *testing.T) {
	srv, calls := fakeGraphQLServer(t)
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	rec := reconciler.New(logger.Discard())
	w := New(rec, cl, logger.Discard())

	settings := func(_ config.Repo) (*config.Settings, error) { return &config.Settings{}, nil }
	if reports := w.ReconcileBatch(context.Background(), nil, settings, true); reports != nil {
		t.Fatalf("expected nil reports, got %d", len(reports))
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("expected 0 graphql calls on empty input, got %d", got)
	}
}

// A failing prefetch must not abort the batch — runOne falls back to
// per-repo state. With nil settings the reconciler still produces a
// report so we can assert continuity.
func TestReconcileBatch_PrefetchFailureTolerated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/graphql" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	rec := reconciler.New(logger.Discard())
	w := New(rec, cl, logger.Discard())

	repos := makeRepos(5)
	settings := func(_ config.Repo) (*config.Settings, error) { return &config.Settings{}, nil }
	reports := w.ReconcileBatch(context.Background(), repos, settings, true)
	if len(reports) != 5 {
		t.Fatalf("expected 5 reports despite prefetch failure, got %d", len(reports))
	}
}

// runOne acquires the per-repo lock so that two concurrent reconciles
// against the same repo never overlap. This is the contract the worker
// relies on to avoid duplicate writes.
func TestReconcileBatch_PerRepoLockSerializes(t *testing.T) {
	srv, _ := fakeGraphQLServer(t)
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	rec := reconciler.New(logger.Discard())
	w := New(rec, cl, logger.Discard())
	w.Concurrency = 8

	const sameRepoCopies = 4
	repos := make([]config.Repo, sameRepoCopies)
	for i := range repos {
		repos[i] = config.Repo{Owner: "o", Name: "shared"}
	}

	// settingsFor blocks until released so we can observe whether two
	// reconciles for the same repo run concurrently. With the lock,
	// only one can be inside settingsFor at a time.
	var inFlight int32
	var maxObserved int32
	gate := make(chan struct{})
	var releaseOnce sync.Once
	settings := func(_ config.Repo) (*config.Settings, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxObserved)
			if n <= cur || atomic.CompareAndSwapInt32(&maxObserved, cur, n) {
				break
			}
		}
		releaseOnce.Do(func() { close(gate) })
		<-gate
		atomic.AddInt32(&inFlight, -1)
		return &config.Settings{}, nil
	}

	reports := w.ReconcileBatch(context.Background(), repos, settings, true)
	if len(reports) != sameRepoCopies {
		t.Fatalf("got %d reports, want %d", len(reports), sameRepoCopies)
	}
	if got := atomic.LoadInt32(&maxObserved); got != 1 {
		t.Fatalf("max concurrent reconciles for same repo = %d, want 1 (lock should serialize)", got)
	}
}
