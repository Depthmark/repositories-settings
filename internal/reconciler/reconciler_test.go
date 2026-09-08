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

	rep, err := r.Reconcile(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, config.RepoOnly(settings), config.TriggerManual, false, nil)
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
		config.RepoOnly(&config.Settings{Repo: &config.RepoConfig{Description: &desc}}), config.TriggerManual, true, nil)
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

func TestReconcile_SerializesSameRepository(t *testing.T) {
	var (
		inFlight atomic.Int32
		maxSeen  atomic.Int32
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
		_ = json.NewEncoder(w).Encode(map[string]any{"description": "live"})
	}))
	defer srv.Close()

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 10})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
	r := New(logger.Discard())
	repo := config.Repo{Owner: "o", Name: "r"}
	res := config.RepoOnly(&config.Settings{
		Repo: &config.RepoConfig{Description: config.Ptr("live")},
	})

	errCh := make(chan error, 4)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Reconcile(context.Background(), cl, repo, res, config.TriggerManual, true, nil)
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
	}
	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("max concurrent GitHub calls for one repo = %d, want 1", got)
	}
}

// Operator-disabled lanes: when the repo's YAML defines a section
// the operator has globally disabled, the reconciler must NOT call
// any GitHub endpoint for that lane and must record one synthetic
// Skipped Result so the user sees what got dropped.
func TestReconcile_DisabledLanesAreSkippedNotApplied(t *testing.T) {
	var pagesHits int32
	var secretsHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/o/r" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"description": "x"})
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/pages"):
			atomic.AddInt32(&pagesHits, 1)
			w.WriteHeader(404)
		case strings.HasPrefix(r.URL.Path, "/repos/o/r/actions/secrets"):
			atomic.AddInt32(&secretsHits, 1)
			w.WriteHeader(404)
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

	build := "workflow"
	settings := &config.Settings{
		Pages:   &config.PagesConfig{BuildType: &build},
		Secrets: &config.SecretsConfig{RepositorySecrets: []config.SecretRef{{Name: "FOO"}}},
	}

	disabled, _ := config.ParseDisabledResources("pages,secrets")
	r := New(logger.Discard()).WithDisabled(disabled)
	rep, err := r.Reconcile(context.Background(), cl,
		config.Repo{Owner: "o", Name: "r"}, config.RepoOnly(settings), config.TriggerManual, false, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Zero traffic to the disabled endpoints — the lane must not
	// even read live state.
	if got := atomic.LoadInt32(&pagesHits); got != 0 {
		t.Errorf("pages endpoint hit %d times despite being disabled", got)
	}
	if got := atomic.LoadInt32(&secretsHits); got != 0 {
		t.Errorf("secrets endpoint hit %d times despite being disabled", got)
	}

	// Exactly one Skipped Result per disabled-and-configured lane.
	skipped := map[string]bool{}
	for _, res := range rep.Applied {
		if res.Action != "skipped" {
			continue
		}
		if !res.Success {
			t.Errorf("Skipped Result must be Success=true so it doesn't trip 'failure' conclusion: %+v", res)
		}
		if res.Error != "disabled by operator policy" {
			t.Errorf("unexpected Skipped reason: %q", res.Error)
		}
		skipped[res.Resource] = true
	}
	if !skipped["pages"] || !skipped["secrets"] {
		t.Fatalf("expected Skipped Results for pages + secrets, got %v", skipped)
	}
}

// Disabling a lane the repo never configured is a noop — no Skipped
// Result, no GitHub calls. We don't want unconfigured lanes spamming
// the report with skipped entries every reconcile.
func TestReconcile_DisabledButUnconfiguredEmitsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 1})
	defer rl.Stop()
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})

	disabled, _ := config.ParseDisabledResources("pages,secrets,deploy_keys")
	r := New(logger.Discard()).WithDisabled(disabled)
	rep, err := r.Reconcile(context.Background(), cl,
		config.Repo{Owner: "o", Name: "r"}, config.RepoOnly(&config.Settings{}), config.TriggerManual, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range rep.Applied {
		if res.Action == "skipped" {
			t.Fatalf("unexpected Skipped Result for unconfigured lane: %+v", res)
		}
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
		config.RepoOnly(&config.Settings{Teams: &config.TeamsConfig{Teams: []config.TeamAccess{{Slug: "p", Permission: "push"}}}}),
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
