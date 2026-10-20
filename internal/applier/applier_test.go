package applier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
)

// Verify NewTeamsLane fans out create+update+delete correctly.
func TestTeamsLane_PlanAndApply(t *testing.T) {
	var puts, deletes int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/teams":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"slug": "stay", "permission": "push"},
				{"slug": "remove", "permission": "pull"},
			})
		case r.Method == http.MethodPut:
			atomic.AddInt32(&puts, 1)
			w.WriteHeader(204)
		case r.Method == http.MethodDelete:
			atomic.AddInt32(&deletes, 1)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 5})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})

	cfg := &config.TeamsConfig{Teams: []config.TeamAccess{
		{Slug: "stay", Permission: "push"},   // noop
		{Slug: "newone", Permission: "pull"}, // create
		// "remove" is implicit delete
	}}
	lane := NewTeamsLane(cfg)
	diffs, results, err := lane.Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]diff.Action{}
	for _, d := range diffs {
		got[d.Resource] = d.Action
	}
	if got["teams.stay"] != diff.Noop {
		t.Errorf("teams.stay = %s want noop", got["teams.stay"])
	}
	if got["teams.newone"] != diff.Create {
		t.Errorf("teams.newone = %s want create", got["teams.newone"])
	}
	if got["teams.remove"] != diff.Delete {
		t.Errorf("teams.remove = %s want delete", got["teams.remove"])
	}
	// 2 actionable mutations -> 1 PUT (create) + 1 DELETE.
	if atomic.LoadInt32(&puts) != 1 {
		t.Fatalf("puts = %d, want 1", puts)
	}
	if atomic.LoadInt32(&deletes) != 1 {
		t.Fatalf("deletes = %d, want 1", deletes)
	}
	successes := 0
	for _, r := range results {
		if r.Success {
			successes++
		}
	}
	if successes != 2 {
		t.Fatalf("successes = %d, want 2", successes)
	}
}

func TestTeamsLane_DryRunSkipsApply(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 5})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
	lane := NewTeamsLane(&config.TeamsConfig{Teams: []config.TeamAccess{{Slug: "x", Permission: "push"}}})
	_, results, err := lane.Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("dry-run produced %d results", len(results))
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("dry-run hit %d write endpoints", calls)
	}
}
