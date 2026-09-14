package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	settings := func(_ config.Repo) (*config.Resolution, error) { return config.RepoOnly(nil), nil }
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

	settings := func(_ config.Repo) (*config.Resolution, error) { return config.RepoOnly(nil), nil }
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
	settings := func(_ config.Repo) (*config.Resolution, error) { return config.RepoOnly(nil), nil }
	reports := w.ReconcileBatch(context.Background(), repos, settings, true)
	if len(reports) != 5 {
		t.Fatalf("expected 5 reports despite prefetch failure, got %d", len(reports))
	}
}

// Reconciler.Reconcile holds the per-repo lock so two worker runs
// against the same repository never overlap. The observation has to be
// made inside the lock: settings are resolved before it is taken, so
// counting concurrency there measures nothing.
func TestReconcileBatch_PerRepoLockSerializes(t *testing.T) {
	var inFlight, maxObserved int32
	released := make(chan struct{})
	var releaseOnce sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/shared" {
			http.NotFound(w, r)
			return
		}
		n := atomic.AddInt32(&inFlight, 1)
		for {
			cur := atomic.LoadInt32(&maxObserved)
			if n <= cur || atomic.CompareAndSwapInt32(&maxObserved, cur, n) {
				break
			}
		}
		// Hold the first reader open long enough that a second one would
		// overlap if the lock were not doing its job. Every reader waits
		// on the same channel, so the test does not serialise itself.
		releaseOnce.Do(func() {
			time.AfterFunc(50*time.Millisecond, func() { close(released) })
		})
		<-released
		atomic.AddInt32(&inFlight, -1)
		_ = json.NewEncoder(w).Encode(map[string]any{"description": "live"})
	}))
	defer srv.Close()

	cl, rl := newTestClient(srv)
	defer rl.Stop()

	w := New(reconciler.New(logger.Discard()), cl, logger.Discard())
	w.Concurrency = 8

	const sameRepoCopies = 4
	repos := make([]config.Repo, sameRepoCopies)
	for i := range repos {
		repos[i] = config.Repo{Owner: "o", Name: "shared"}
	}

	// A repo config makes Phase A read live state, which is the call the
	// fake server counts.
	desc := "live"
	settings := func(_ config.Repo) (*config.Resolution, error) {
		return config.RepoOnly(&config.Settings{
			Repo: &config.RepoConfig{Description: &desc},
		}), nil
	}

	reports := w.ReconcileBatch(context.Background(), repos, settings, true)
	if len(reports) != sameRepoCopies {
		t.Fatalf("got %d reports, want %d", len(reports), sameRepoCopies)
	}
	if got := atomic.LoadInt32(&maxObserved); got != 1 {
		t.Fatalf("max concurrent reads for one repo = %d, want 1 (the lock should serialize them)", got)
	}
}
