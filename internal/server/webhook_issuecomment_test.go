package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

// ghStub is a flexible fake GitHub server: every recorded request
// goes into reqs and the test asserts on those.
type ghStub struct {
	reqs []recordedReq
	srv  *httptest.Server
}

type recordedReq struct {
	Method, Path string
	Body         string
}

func newGHStub(t *testing.T) *ghStub {
	t.Helper()
	g := &ghStub{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.reqs = append(g.reqs, recordedReq{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments") &&
			strings.Contains(r.URL.Path, "/issues/"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/comments"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 555, "body": "ok"})
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/comments/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	}))
	return g
}

func (g *ghStub) close()                     { g.srv.Close() }
func (g *ghStub) requests() []recordedReq    { return append([]recordedReq(nil), g.reqs...) }
func (g *ghStub) count(method, path string) int {
	n := 0
	for _, r := range g.reqs {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func newServer(t *testing.T, gh *ghStub, settings func(context.Context, config.Repo, string) (*config.Settings, error)) *Server {
	t.Helper()
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 4})
	t.Cleanup(rl.Stop)
	cl := ghclient.New(ghclient.Config{
		APIURL: gh.srv.URL, Limiter: rl, HTTPClient: gh.srv.Client(), Logger: logger.Discard(),
	})
	return New(":0", Deps{
		Logger:     logger.Discard(),
		Client:     cl,
		Reconciler: reconciler.New(logger.Discard()),
		AppSlug:    "repo-settings",
		Settings:   settings,
	})
}

func postWebhook(s *Server, event, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader([]byte(payload)))
	req.Header.Set("X-GitHub-Event", event)
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	return rr
}

// recheck on a PR with no managed config exercises the full pipeline:
// Settings load, dry-run reconcile (no-op), comment GET, comment POST.
func TestWebhook_IssueComment_RecheckPostsSticky(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Settings, error) {
		return &config.Settings{}, nil
	}
	s := newServer(t, gh, settings)

	rr := postWebhook(s, "issue_comment",
		buildPayload("@repo-settings recheck", "MEMBER"))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if got := gh.count(http.MethodGet, "/repos/o/r/issues/42/comments"); got != 1 {
		t.Fatalf("expected 1 GET on issues/42/comments, got %d", got)
	}
	if got := gh.count(http.MethodPost, "/repos/o/r/issues/42/comments"); got != 1 {
		t.Fatalf("expected 1 POST sticky, got %d", got)
	}
}

// apply from a contributor without write access is rejected and a
// permission-denied comment is posted instead of a reconcile.
func TestWebhook_IssueComment_ApplyRequiresWriteAccess(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()

	var reconciled int32
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Settings, error) {
		atomic.AddInt32(&reconciled, 1)
		return &config.Settings{}, nil
	}
	s := newServer(t, gh, settings)

	rr := postWebhook(s, "issue_comment",
		buildPayload("@repo-settings apply", "CONTRIBUTOR"))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if atomic.LoadInt32(&reconciled) != 0 {
		t.Fatal("settings loader was called despite unprivileged author")
	}
	// The bot still posts a comment explaining the rejection.
	if got := gh.count(http.MethodPost, "/repos/o/r/issues/42/comments"); got != 1 {
		t.Fatalf("expected 1 POST refusing apply, got %d", got)
	}
}

// Bot-authored comments are skipped to break feedback loops.
func TestWebhook_IssueComment_IgnoresBotAuthor(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Settings, error) {
		return &config.Settings{}, nil
	}
	s := newServer(t, gh, settings)

	payload := `{
      "action":"created",
      "repository":{"name":"r","owner":{"login":"o"}},
      "issue":{"number":42,"pull_request":{"url":"x"}},
      "comment":{"body":"@repo-settings recheck","author_association":"NONE","user":{"login":"repo-settings[bot]","type":"Bot"}}
    }`
	rr := postWebhook(s, "issue_comment", payload)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	for _, r := range gh.requests() {
		if strings.Contains(r.Path, "/issues/") {
			t.Fatalf("bot comment must not trigger any GitHub call, saw %v", r)
		}
	}
}

// Configuration errors from the loader are surfaced as a sticky comment
// (and the HTTP response is still 200 with the error in the body).
func TestWebhook_PullRequest_ConfigErrorPostsComment(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Settings, error) {
		return nil, &config.ValidationError{File: "teams.yml", Issues: []string{"missing required field 'slug'"}}
	}
	s := newServer(t, gh, settings)

	payload := `{
      "action":"opened",
      "number":7,
      "repository":{"name":"r","owner":{"login":"o"}},
      "pull_request":{"number":7,"head":{"ref":"feature","sha":"deadbeef"}}
    }`
	rr := postWebhook(s, "pull_request", payload)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	if got := gh.count(http.MethodPost, "/repos/o/r/issues/7/comments"); got != 1 {
		t.Fatalf("expected 1 POST with config errors, got %d (reqs=%v)", got, gh.requests())
	}
	// Body should mention the offending file so the contributor knows
	// where to look.
	var sawTeamsYml bool
	for _, r := range gh.requests() {
		if r.Method == http.MethodPost && strings.Contains(r.Body, "teams.yml") {
			sawTeamsYml = true
		}
	}
	if !sawTeamsYml {
		t.Fatal("config-error comment didn't mention the bad file")
	}
}

// Help text fires when the bot is mentioned with no recognised verb.
func TestWebhook_IssueComment_HelpOnBareMention(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Settings, error) {
		return &config.Settings{}, nil
	}
	s := newServer(t, gh, settings)
	// Trigger on its own line with no verb → help. Casual mentions
	// embedded in prose are intentionally ignored, so the trigger must
	// be at the start of a line.
	rr := postWebhook(s, "issue_comment", buildPayload("@repo-settings", "MEMBER"))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	// Help still posts a comment even though no command ran.
	if got := gh.count(http.MethodPost, "/repos/o/r/issues/42/comments"); got != 1 {
		t.Fatalf("expected 1 POST help, got %d", got)
	}
}

// Sanity: ParseComment / IsConfigError are wired through the
// flattenErrors helper for joined errors.
func TestIsConfigError_HandlesJoinedErrors(t *testing.T) {
	leaf := &config.ValidationError{File: "x.yml", Issues: []string{"oops"}}
	wrapped := errors.Join(errors.New("preamble"), leaf)
	if !isConfigError(wrapped) {
		t.Fatal("expected joined ValidationError to be detected")
	}
	if isConfigError(errors.New("plain")) {
		t.Fatal("plain error should not be a config error")
	}
}

// buildPayload composes a minimal issue_comment payload with a
// configurable comment body and author_association.
func buildPayload(body, association string) string {
	return marshalJSON(map[string]any{
		"action":     "created",
		"repository": map[string]any{"name": "r", "owner": map[string]any{"login": "o"}},
		"issue":      map[string]any{"number": 42, "pull_request": map[string]any{"url": "x"}},
		"comment": map[string]any{
			"body":               body,
			"author_association": association,
			"user":               map[string]any{"login": "alice", "type": "User"},
		},
	})
}

func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
