package applier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
)

// ---------- shared helpers ---------------------------------------------------

// recordedReq captures the bits of a request applier tests typically
// assert against. We avoid hauling around full *http.Request values so
// the tests don't have to manage body lifetimes.
type recordedReq struct {
	Method string
	Path   string
	Body   []byte
}

type recorder struct {
	mu   sync.Mutex
	reqs []recordedReq
}

func (rec *recorder) record(r *http.Request) recordedReq {
	body := []byte(nil)
	if r.Body != nil {
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 4096)
		for {
			n, err := r.Body.Read(tmp)
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err != nil {
				break
			}
		}
		body = buf
	}
	rr := recordedReq{Method: r.Method, Path: r.URL.Path, Body: body}
	rec.mu.Lock()
	rec.reqs = append(rec.reqs, rr)
	rec.mu.Unlock()
	return rr
}

// methodPath returns the count of recorded requests matching method + path.
func (rec *recorder) count(method, path string) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	n := 0
	for _, r := range rec.reqs {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// newTestClient wraps a server URL into a ghclient.Client suitable for
// applier tests (App auth disabled — tests pass plain owner strings).
func newTestClient(srv *httptest.Server) (*ghclient.Client, *ghclient.RateLimiter) {
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 5})
	cl := ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
	return cl, rl
}

// firstActionable returns the first non-noop diff or fails the test.
func firstActionable(t *testing.T, diffs []diff.Diff) diff.Diff {
	t.Helper()
	for _, d := range diffs {
		if d.Action != diff.Noop {
			return d
		}
	}
	t.Fatalf("no actionable diff in %+v", diffs)
	return diff.Diff{}
}

// countSuccess returns the number of Result entries with Success==true.
func countSuccess(rs []Result) int {
	n := 0
	for _, r := range rs {
		if r.Success {
			n++
		}
	}
	return n
}

// ---------- Phase A: repo ----------------------------------------------------

func TestRepoLane_PlanAndApply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"description": "old", "private": false, "topics": []string{"a"},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r":
			w.WriteHeader(200)
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/topics":
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cl, rl := newTestClient(srv)
	defer rl.Stop()

	desc := "new"
	cfg := &config.Settings{
		Repo:   &config.RepoConfig{Description: &desc},
		Topics: ptrStrings("a", "b"),
	}
	lane := NewRepoLane(cfg, nil)
	diffs, results, err := lane.Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 2 {
		t.Fatalf("got %d diffs, want 2 (repo + topics)", len(diffs))
	}
	if rec.count(http.MethodPatch, "/repos/o/r") != 1 {
		t.Fatalf("expected 1 PATCH /repos/o/r, got %d", rec.count(http.MethodPatch, "/repos/o/r"))
	}
	if rec.count(http.MethodPut, "/repos/o/r/topics") != 1 {
		t.Fatalf("expected 1 PUT topics, got %d", rec.count(http.MethodPut, "/repos/o/r/topics"))
	}
	if countSuccess(results) != 2 {
		t.Fatalf("successes = %d, want 2", countSuccess(results))
	}
}

// Skipping the REST GET via prefetched live state must yield the same
// plan result without ever calling /repos/o/r.
func TestRepoLane_PrefetchedSkipsRESTGet(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(200) // PATCH path; should not see GET
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	desc := "x"
	cfg := &config.Settings{Repo: &config.RepoConfig{Description: &desc}}
	pf := &PrefetchedRepo{Description: "y"}
	_, results, err := NewRepoLane(cfg, pf).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodGet, "/repos/o/r") != 0 {
		t.Fatal("expected zero REST GETs when prefetched live state is supplied")
	}
	if countSuccess(results) != 1 {
		t.Fatalf("successes = %d, want 1 (PATCH only)", countSuccess(results))
	}
}

func ptrStrings(s ...string) *[]string { return &s }

// ---------- rulesets ---------------------------------------------------------

func TestRulesetsLane_PlanAndApply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			// FetchRepoRulesetIDs replaces the REST list endpoint.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"repository": map[string]any{
						"rulesets": map[string]any{
							"nodes": []map[string]any{
								{"databaseId": 7, "name": "stay"},
								{"databaseId": 8, "name": "remove"},
							},
							"pageInfo": map[string]any{"hasNextPage": false},
						},
					},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/rulesets/7":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "stay", "target": "branch", "enforcement": "active", "rules": []any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/rulesets/8":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "remove", "target": "branch", "enforcement": "active", "rules": []any{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/rulesets":
			w.WriteHeader(201)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/rulesets/8":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.RulesetsConfig{Rulesets: []config.Ruleset{
		{Name: "stay", Target: "branch", Enforcement: "active", Rules: []config.RulesetRule{}},
		{Name: "newone", Target: "branch", Enforcement: "active", Rules: []config.RulesetRule{}},
	}}
	_, results, err := NewRulesetsLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPost, "/repos/o/r/rulesets") != 1 {
		t.Fatalf("expected 1 create POST")
	}
	if rec.count(http.MethodDelete, "/repos/o/r/rulesets/8") != 1 {
		t.Fatalf("expected 1 delete")
	}
	if countSuccess(results) != 2 {
		t.Fatalf("successes = %d, want 2", countSuccess(results))
	}
}

// ---------- environments -----------------------------------------------------

func TestEnvironmentsLane_PlanAndApply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/environments":
			_ = json.NewEncoder(w).Encode(map[string]any{"environments": []map[string]any{
				{"name": "prod"},
				{"name": "stage"},
			}})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/environments/prod":
			w.WriteHeader(200)
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/environments/qa":
			w.WriteHeader(200)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/environments/stage":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	wait := 5
	cfg := &config.EnvironmentsConfig{Environments: []config.Environment{
		{Name: "prod", WaitTimer: &wait}, // update
		{Name: "qa"},                     // create
	}}
	_, results, err := NewEnvironmentsLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPut, "/repos/o/r/environments/prod") != 1 {
		t.Fatal("expected PUT prod")
	}
	if rec.count(http.MethodPut, "/repos/o/r/environments/qa") != 1 {
		t.Fatal("expected PUT qa")
	}
	if rec.count(http.MethodDelete, "/repos/o/r/environments/stage") != 1 {
		t.Fatal("expected DELETE stage")
	}
	if countSuccess(results) != 3 {
		t.Fatalf("successes = %d, want 3", countSuccess(results))
	}
}

// ---------- webhooks ---------------------------------------------------------

func TestWebhooksLane_PlanAndApply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/hooks":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "active": true, "events": []string{"push"}, "config": map[string]any{
					"url": "https://stay.example", "content_type": "json", "insecure_ssl": "0",
				}},
				{"id": 2, "active": true, "events": []string{"push"}, "config": map[string]any{
					"url": "https://gone.example", "content_type": "json", "insecure_ssl": "0",
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/hooks":
			w.WriteHeader(201)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/hooks/2":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.WebhooksConfig{Webhooks: []config.Webhook{
		{URL: "https://stay.example", ContentType: "json", Active: true, Events: []string{"push"}}, // noop
		{URL: "https://new.example", ContentType: "json", Active: true, Events: []string{"push"}},  // create
	}}
	_, results, err := NewWebhooksLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPost, "/repos/o/r/hooks") != 1 {
		t.Fatal("expected one create POST")
	}
	if rec.count(http.MethodDelete, "/repos/o/r/hooks/2") != 1 {
		t.Fatal("expected one delete")
	}
	if countSuccess(results) != 2 {
		t.Fatalf("successes = %d, want 2", countSuccess(results))
	}
}

// ---------- autolinks --------------------------------------------------------

// Update is implemented as delete+recreate, so a single update produces
// two HTTP calls but still one Result.
func TestAutolinksLane_UpdateIsDeletePlusCreate(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/autolinks":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 9, "key_prefix": "JIRA-", "url_template": "https://old.example/<num>", "is_alphanumeric": false},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/autolinks/9":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/autolinks":
			w.WriteHeader(201)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.AutolinksConfig{Autolinks: []config.Autolink{
		{KeyPrefix: "JIRA-", URLTemplate: "https://new.example/<num>"},
	}}
	_, results, err := NewAutolinksLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodDelete, "/repos/o/r/autolinks/9") != 1 {
		t.Fatal("expected delete during update")
	}
	if rec.count(http.MethodPost, "/repos/o/r/autolinks") != 1 {
		t.Fatal("expected create during update")
	}
	if len(results) != 1 || !results[0].Success || results[0].Action != Updated {
		t.Fatalf("expected one Updated result, got %+v", results)
	}
}

// ---------- actions ----------------------------------------------------------

func TestActionsLane_PlanAndApply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/permissions":
			_ = json.NewEncoder(w).Encode(map[string]any{"enabled": false, "allowed_actions": "selected"})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/actions/permissions":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	enabled := true
	cfg := &config.ActionsConfig{Enabled: &enabled}
	diffs, results, err := NewActionsLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	d := firstActionable(t, diffs)
	if d.Action != diff.Update {
		t.Fatalf("action = %s, want update", d.Action)
	}
	if rec.count(http.MethodPut, "/repos/o/r/actions/permissions") != 1 {
		t.Fatal("expected PUT actions/permissions")
	}
	if countSuccess(results) != 1 {
		t.Fatalf("successes = %d, want 1", countSuccess(results))
	}
}

// ---------- security --------------------------------------------------------

func TestSecurityLane_PlanAndApply(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{"security_and_analysis": map[string]any{
				"secret_scanning": map[string]any{"status": "disabled"},
			}})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r":
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	on := true
	cfg := &config.SecurityConfig{SecretScanning: &on}
	_, results, err := NewSecurityLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPatch, "/repos/o/r") != 1 {
		t.Fatal("expected PATCH on repo")
	}
	if countSuccess(results) != 1 {
		t.Fatalf("successes = %d, want 1", countSuccess(results))
	}
}

// ---------- pages -----------------------------------------------------------

// 404 from GET pages means the site doesn't exist yet; the lane must
// treat that as "nothing live" and POST to create.
func TestPagesLane_CreatesWhenAbsent(t *testing.T) {
	var posts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pages":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pages":
			atomic.AddInt32(&posts, 1)
			w.WriteHeader(201)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	build := "workflow"
	cfg := &config.PagesConfig{BuildType: &build}
	_, results, err := NewPagesLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&posts) != 1 {
		t.Fatalf("expected 1 POST pages, got %d", posts)
	}
	if len(results) != 1 || !results[0].Success || results[0].Action != Created {
		t.Fatalf("expected 1 Created result, got %+v", results)
	}
}

// ---------- secrets ---------------------------------------------------------

// Secrets only manage names. A name in desired but not live yields a
// failed Result (we can't create without the value); a name in live
// but not desired yields a delete.
func TestSecretsLane_DeleteAndCreateBehavior(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/secrets":
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []map[string]any{{"name": "STAY"}, {"name": "GONE"}}})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/actions/secrets/GONE":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.SecretsConfig{RepositorySecrets: []config.SecretRef{
		{Name: "STAY"},   // noop
		{Name: "NEWONE"}, // create -> failure (no value)
	}}
	_, results, err := NewSecretsLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodDelete, "/repos/o/r/actions/secrets/GONE") != 1 {
		t.Fatal("expected DELETE GONE")
	}
	// 1 success (delete) + 1 failure (create-without-value).
	wantSuccess, wantFail := 1, 1
	gotSuccess, gotFail := 0, 0
	for _, r := range results {
		if r.Success {
			gotSuccess++
		} else {
			gotFail++
		}
	}
	if gotSuccess != wantSuccess || gotFail != wantFail {
		t.Fatalf("results breakdown success=%d fail=%d, want %d/%d", gotSuccess, gotFail, wantSuccess, wantFail)
	}
}

// ---------- variables -------------------------------------------------------

func TestVariablesLane_CreateUpdateDelete(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/variables":
			_ = json.NewEncoder(w).Encode(map[string]any{"variables": []map[string]any{
				{"name": "EXISTING", "value": "old"},
				{"name": "GONE", "value": "x"},
			}})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/actions/variables/EXISTING":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/actions/variables":
			w.WriteHeader(201)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/actions/variables/GONE":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.VariablesConfig{Variables: []config.Variable{
		{Name: "EXISTING", Value: "new"}, // update
		{Name: "FRESH", Value: "yes"},    // create
	}}
	_, results, err := NewVariablesLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPatch, "/repos/o/r/actions/variables/EXISTING") != 1 {
		t.Fatal("expected PATCH EXISTING")
	}
	if rec.count(http.MethodPost, "/repos/o/r/actions/variables") != 1 {
		t.Fatal("expected POST variables")
	}
	if rec.count(http.MethodDelete, "/repos/o/r/actions/variables/GONE") != 1 {
		t.Fatal("expected DELETE GONE")
	}
	if countSuccess(results) != 3 {
		t.Fatalf("successes = %d, want 3", countSuccess(results))
	}
}

// ---------- deploy keys -----------------------------------------------------

// Deploy keys can't be PATCHed; an update is delete+create, surfacing
// as one Updated Result.
func TestDeployKeysLane_UpdateIsDeletePlusCreate(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/keys":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 11, "title": "ci", "read_only": true},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/keys/11":
			w.WriteHeader(204)
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/keys":
			w.WriteHeader(201)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.DeployKeysConfig{DeployKeys: []config.DeployKey{
		{Title: "ci", Key: "ssh-ed25519 AAAA", ReadOnly: false}, // flip read_only
	}}
	_, results, err := NewDeployKeysLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodDelete, "/repos/o/r/keys/11") != 1 {
		t.Fatal("expected delete during update")
	}
	if rec.count(http.MethodPost, "/repos/o/r/keys") != 1 {
		t.Fatal("expected create during update")
	}
	if len(results) != 1 || !results[0].Success || results[0].Action != Updated {
		t.Fatalf("expected one Updated result, got %+v", results)
	}
}

// ---------- custom properties ------------------------------------------------

func TestCustomPropertiesLane_PatchesValues(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/properties/values":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"property_name": "tier", "value": "bronze"},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/properties/values":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.CustomPropertiesConfig{Properties: map[string]any{"tier": "gold"}}
	_, results, err := NewCustomPropertiesLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPatch, "/repos/o/r/properties/values") != 1 {
		t.Fatal("expected PATCH properties/values")
	}
	if countSuccess(results) != 1 {
		t.Fatalf("successes = %d, want 1", countSuccess(results))
	}
}

// ---------- collaborators ----------------------------------------------------

func TestCollaboratorsLane_AddRemove(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/collaborators":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"login": "alice", "role_name": "push"},
				{"login": "bob", "role_name": "admin"},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/collaborators/carol":
			w.WriteHeader(204)
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/collaborators/bob":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	cfg := &config.CollaboratorsConfig{Collaborators: []config.Collaborator{
		{Username: "alice", Permission: config.PermPush}, // noop
		{Username: "carol", Permission: config.PermPull}, // create
	}}
	_, results, err := NewCollaboratorsLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rec.count(http.MethodPut, "/repos/o/r/collaborators/carol") != 1 {
		t.Fatal("expected PUT carol")
	}
	if rec.count(http.MethodDelete, "/repos/o/r/collaborators/bob") != 1 {
		t.Fatal("expected DELETE bob")
	}
	if countSuccess(results) != 2 {
		t.Fatalf("successes = %d, want 2", countSuccess(results))
	}
}

// ---------- branches ---------------------------------------------------------

// Branches: legacy protection API. The lane diffs each pattern
// independently — 404 on GET means unprotected (no current state) and
// the desired config triggers a create-style PUT.
func TestBranchesLane_CreatesProtectionFromAbsent(t *testing.T) {
	var puts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/repos/o/r/branches/main/protection":
			atomic.AddInt32(&puts, 1)
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	enforce := true
	cfg := &config.BranchesConfig{Branches: []config.BranchProtection{
		{Pattern: "main", EnforceAdmins: &enforce},
	}}
	_, results, err := NewBranchesLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&puts) != 1 {
		t.Fatalf("expected 1 PUT, got %d", puts)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("expected one success, got %+v", results)
	}
}

// Dry-run on branches must skip every PUT — the lane returns diffs but
// no Result entries.
func TestBranchesLane_DryRunNoWrites(t *testing.T) {
	var writes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		atomic.AddInt32(&writes, 1)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cl, rl := newTestClient(srv)
	defer rl.Stop()

	enforce := true
	cfg := &config.BranchesConfig{Branches: []config.BranchProtection{
		{Pattern: "main", EnforceAdmins: &enforce},
	}}
	diffs, results, err := NewBranchesLane(cfg).Run(context.Background(), cl, config.Repo{Owner: "o", Name: "r"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) == 0 {
		t.Fatal("expected at least one diff")
	}
	if len(results) != 0 {
		t.Fatalf("dry-run produced %d results", len(results))
	}
	if atomic.LoadInt32(&writes) != 0 {
		t.Fatalf("dry-run hit %d write endpoints", writes)
	}
}
