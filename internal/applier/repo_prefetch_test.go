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

// repoLaneServer answers GET /repos/o/r with a full REST document and
// counts how many times it was asked.
func repoLaneServer(t *testing.T, gets *int32) *ghclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/o/r" {
			atomic.AddInt32(gets, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"description":        "live",
				"allow_squash_merge": true,
				"topics":             []string{"go"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 5})
	t.Cleanup(rl.Stop)
	return ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
}

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

// The cron batch query fetches a subset of the repository document. When
// the configuration stays inside that subset the prefetched record is
// used and no REST call happens; when it reaches outside, the lane must
// fall back to REST rather than diff configured fields against the zero
// values the batch query never filled in.
func TestRepoLane_PrefetchOnlyUsedWhenItCoversTheConfig(t *testing.T) {
	prefetched := &PrefetchedRepo{
		Description:    "live",
		Topics:         []string{"go"},
		TopicsComplete: true,
	}

	t.Run("covered config skips REST", func(t *testing.T) {
		var gets int32
		cl := repoLaneServer(t, &gets)
		lane := NewRepoLane(&config.Settings{
			Repo: &config.RepoConfig{Description: strPtr("live")},
		}, prefetched)

		diffs, _, err := lane.Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if gets != 0 {
			t.Errorf("prefetched record covers the config; expected 0 REST reads, got %d", gets)
		}
		if len(diffs) != 1 || diffs[0].Action != diff.Noop {
			t.Errorf("description already matches; expected a noop, got %+v", diffs)
		}
	})

	t.Run("uncovered field falls back to REST", func(t *testing.T) {
		var gets int32
		cl := repoLaneServer(t, &gets)
		// allow_squash_merge is not in the batch query. Using the
		// prefetched record would compare desired true against a zero
		// false and report drift that does not exist.
		lane := NewRepoLane(&config.Settings{
			Repo: &config.RepoConfig{AllowSquashMerge: boolPtr(true)},
		}, prefetched)

		diffs, _, err := lane.Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if gets != 1 {
			t.Fatalf("expected a REST fallback read, got %d", gets)
		}
		if len(diffs) != 1 || diffs[0].Action != diff.Noop {
			t.Errorf("live state already allows squash merge; expected a noop, got %+v", diffs)
		}
	})

	t.Run("truncated topics fall back to REST", func(t *testing.T) {
		var gets int32
		cl := repoLaneServer(t, &gets)
		truncated := *prefetched
		truncated.TopicsComplete = false
		topics := []string{"go"}
		lane := NewRepoLane(&config.Settings{Topics: &topics}, &truncated)

		if _, _, err := lane.Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, true); err != nil {
			t.Fatal(err)
		}
		if gets != 1 {
			t.Fatalf("a truncated topic list must not be diffed; expected a REST read, got %d", gets)
		}
	})
}
