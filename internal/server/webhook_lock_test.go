package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

// Two deliveries for the same repo must not reconcile concurrently.
//
// This is the routine case, not a rare one: merging a settings PR fires
// pull_request.closed and push within the same second, and interleaved
// PATCHes against the same endpoints leave the repo in a state neither
// run intended.
func TestWebhook_SerializesReconcilesPerRepo(t *testing.T) {
	var (
		inFlight atomic.Int32
		maxSeen  atomic.Int32
	)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		// Hold the request open long enough for a concurrent reconcile
		// to overlap if the lock is not doing its job.
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		_ = json.NewEncoder(w).Encode(map[string]any{"description": "x", "topics": []string{}})
	}))
	defer gh.Close()

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 10})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL: gh.URL, Limiter: rl, HTTPClient: gh.Client(), Logger: logger.Discard(),
	})

	deps := Deps{
		Logger:               logger.Discard(),
		Client:               cl,
		Reconciler:           reconciler.New(logger.Discard()),
		AllowUnauthenticated: true,
		Settings: func(context.Context, config.Repo, string) (*config.Resolution, error) {
			return config.RepoOnly(&config.Settings{Repo: &config.RepoConfig{Description: config.Ptr("y")}}), nil
		},
	}
	h := &webhookHandler{deps: deps}

	body := `{"ref":"refs/heads/main","repository":{"owner":{"login":"o"},"name":"r","default_branch":"main"}}`
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(body))
			req.Header.Set("X-GitHub-Event", "push")
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()

	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("max concurrent GitHub calls for one repo = %d, want 1 (per-repo lock should serialize)", got)
	}
}

func TestAPIAndWebhook_SerializeReconcilesAcrossCaseVariants(t *testing.T) {
	var (
		inFlight atomic.Int32
		maxSeen  atomic.Int32
	)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		_ = json.NewEncoder(w).Encode(map[string]any{"description": "x", "topics": []string{}})
	}))
	defer gh.Close()

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 10})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL: gh.URL, Limiter: rl, HTTPClient: gh.Client(), Logger: logger.Discard(),
	})
	s := New(":0", Deps{
		Logger:               logger.Discard(),
		Client:               cl,
		Reconciler:           reconciler.New(logger.Discard()),
		APIToken:             "token",
		AllowUnauthenticated: true,
		Settings: func(context.Context, config.Repo, string) (*config.Resolution, error) {
			return config.RepoOnly(&config.Settings{Repo: &config.RepoConfig{Description: config.Ptr("y")}}), nil
		},
	})

	var wg sync.WaitGroup
	statuses := make(chan int, 4)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/api/reconcile", strings.NewReader(`{"owner":"o","repo":"r"}`))
			req.Header.Set("Authorization", "Bearer token")
			if i%2 == 0 {
				req = httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{"ref":"refs/heads/main","repository":{"owner":{"login":"O"},"name":"R","default_branch":"main"}}`))
				req.Header.Set("X-GitHub-Event", "push")
			}
			rr := httptest.NewRecorder()
			s.httpSrv.Handler.ServeHTTP(rr, req)
			statuses <- rr.Code
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Errorf("/api/reconcile status = %d, want 200", status)
		}
	}
	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("max concurrent GitHub calls for one repo = %d, want 1 (reconciler lock should serialize)", got)
	}
}
