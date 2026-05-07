package reconciler

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
)

// fakeGitHub is a minimal in-process server that pretends to be GitHub
// for the resources the test exercises.
type fakeGitHub struct {
	mu sync.Mutex

	repoTopics []string
	repo       map[string]any
	teams      []map[string]any

	patchedRepoBody []byte
	putTopicsBody   []byte
	putTeamCalls    int32
}

func (f *fakeGitHub) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r":
			body := map[string]any{
				"description": "before",
				"topics":      f.repoTopics,
				"has_issues":  false,
				"archived":    false,
				"private":     false,
			}
			for k, v := range f.repo {
				body[k] = v
			}
			_ = json.NewEncoder(w).Encode(body)
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r":
			b := readBody(r)
			f.patchedRepoBody = b
			w.WriteHeader(200)
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/topics":
			f.putTopicsBody = readBody(r)
			w.WriteHeader(200)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/teams":
			_ = json.NewEncoder(w).Encode(f.teams)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/orgs/o/teams/"):
			atomic.AddInt32(&f.putTeamCalls, 1)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
		}
	}
}

func readBody(r *http.Request) []byte {
	b := make([]byte, r.ContentLength)
	_, _ = r.Body.Read(b)
	return b
}

func TestReconcile_PhaseAUpdatesRepoAndTopics(t *testing.T) {
	f := &fakeGitHub{
		repoTopics: []string{"old"},
		teams:      []map[string]any{},
	}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 5})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL:     srv.URL,
		Limiter:    rl,
		HTTPClient: srv.Client(),
		Logger:     logger.Discard(),
	})
	r := New(logger.Discard())

	desc := "after"
	hi := true
	topics := []string{"go", "infra"}
	settings := &config.Settings{
		Repo:   &config.RepoConfig{Description: &desc, HasIssues: &hi},
		Topics: &topics,
		Teams: &config.TeamsConfig{
			Teams: []config.TeamAccess{{Slug: "platform", Permission: "push"}},
		},
	}

	rep, err := r.Reconcile(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, settings, config.TriggerManual, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.DryRun {
		t.Fatal("expected dryRun=false")
	}
	if len(f.patchedRepoBody) == 0 {
		t.Fatal("repo PATCH not sent")
	}
	if !strings.Contains(string(f.patchedRepoBody), "after") {
		t.Fatalf("PATCH body missing description: %s", f.patchedRepoBody)
	}
	if !strings.Contains(string(f.putTopicsBody), "go") {
		t.Fatalf("PUT topics missing 'go': %s", f.putTopicsBody)
	}
	if atomic.LoadInt32(&f.putTeamCalls) != 1 {
		t.Fatalf("expected 1 team PUT, got %d", f.putTeamCalls)
	}

	// Diffs should include repository + topics + teams.platform.
	resources := map[string]bool{}
	for _, d := range rep.Diffs {
		resources[d.Resource] = true
	}
	if !resources["repository"] || !resources["topics"] || !resources["teams.platform"] {
		t.Fatalf("missing expected diff resources: %+v", resources)
	}
}

func TestReconcile_DryRunSkipsApply(t *testing.T) {
	f := &fakeGitHub{teams: []map[string]any{}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 5})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
	r := New(logger.Discard())
	desc := "x"
	rep, err := r.Reconcile(context.Background(), cl, config.Repo{Owner: "o", Name: "r"},
		&config.Settings{Repo: &config.RepoConfig{Description: &desc}}, config.TriggerManual, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.DryRun {
		t.Fatal("expected dryRun=true")
	}
	if len(f.patchedRepoBody) != 0 {
		t.Fatal("PATCH should not have fired in dry-run")
	}
	if len(rep.Applied) != 0 {
		t.Fatalf("dry-run produced apply results: %+v", rep.Applied)
	}
}

func TestReconcile_LaneFailureIsolated(t *testing.T) {
	// teams endpoint returns 500; reconcile should still finish and
	// produce a synthetic failure for that lane.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"description": "x"})
		case r.URL.Path == "/repos/o/r/teams":
			w.WriteHeader(500)
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
	r := New(logger.Discard())
	rep, err := r.Reconcile(context.Background(), cl, config.Repo{Owner: "o", Name: "r"},
		&config.Settings{Teams: &config.TeamsConfig{Teams: []config.TeamAccess{{Slug: "p", Permission: "push"}}}},
		config.TriggerManual, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasFailure(rep.Applied) {
		t.Fatal("expected synthetic failure")
	}
	// Ensure we didn't run out — duration should be a positive number.
	if rep.Duration <= 0 {
		t.Fatal("duration not recorded")
	}
	_ = time.Second // silence unused import if any
}
