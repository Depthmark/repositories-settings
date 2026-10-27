package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

// ghStub is a flexible fake GitHub server: every recorded request
// goes into reqs and the test asserts on those. Set the fields before
// the first webhook to shape its answers.
type ghStub struct {
	reqs []recordedReq
	srv  *httptest.Server

	// headSHA is the head of pull request 42; a non-zero pullStatus
	// fails its lookup with that status instead.
	headSHA    string
	pullStatus int
	// permission and roleName answer the collaborator permission lookup;
	// a non-zero permStatus fails it with that status instead.
	permission, roleName string
	permStatus           int
}

type recordedReq struct {
	Method, Path string
	Body         string
}

func newGHStub(t *testing.T) *ghStub {
	t.Helper()
	g := &ghStub{headSHA: "headsha", permission: "write", roleName: "write"}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.reqs = append(g.reqs, recordedReq{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{"description": "live", "topics": []string{}})
		case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls/42":
			if g.pullStatus != 0 {
				http.Error(w, `{"message":"lookup failed"}`, g.pullStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 42, "head": map[string]any{"sha": g.headSHA}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/o/r/collaborators/") &&
			strings.HasSuffix(r.URL.Path, "/permission"):
			if g.permStatus != 0 {
				http.Error(w, `{"message":"lookup failed"}`, g.permStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"permission": g.permission, "role_name": g.roleName})
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

func (g *ghStub) close()                  { g.srv.Close() }
func (g *ghStub) requests() []recordedReq { return append([]recordedReq(nil), g.reqs...) }
func (g *ghStub) count(method, path string) int {
	n := 0
	for _, r := range g.reqs {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

func newServer(t *testing.T, gh *ghStub, settings func(context.Context, config.Repo, string) (*config.Resolution, error)) *Server {
	t.Helper()
	return newServerWithLogger(t, gh, settings, logger.Discard())
}

func newServerWithLogger(t *testing.T, gh *ghStub, settings func(context.Context, config.Repo, string) (*config.Resolution, error), log *slog.Logger) *Server {
	t.Helper()
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 4})
	t.Cleanup(rl.Stop)
	cl := ghclient.New(ghclient.Config{
		APIURL: gh.srv.URL, Limiter: rl, HTTPClient: gh.srv.Client(), Logger: logger.Discard(),
	})
	return New(":0", Deps{
		Logger:               log,
		Client:               cl,
		Reconciler:           reconciler.New(logger.Discard()),
		AllowUnauthenticated: true,
		AppSlug:              "repo-settings",
		Settings:             settings,
	})
}

func postWebhook(s *Server, event, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader([]byte(payload)))
	req.Header.Set("X-GitHub-Event", event)
	rr := httptest.NewRecorder()
	s.httpSrv.Handler.ServeHTTP(rr, req)
	return rr
}

// settingsByRef serves a different repository description at the PR
// head than on the default branch, so a test can tell from the sticky
// comment which ref was planned. It records every ref it was asked for.
func settingsByRef(refs *[]string) func(context.Context, config.Repo, string) (*config.Resolution, error) {
	return func(_ context.Context, _ config.Repo, ref string) (*config.Resolution, error) {
		*refs = append(*refs, ref)
		desc := "from-main"
		if ref == "headsha" {
			desc = "from-head"
		}
		return config.RepoOnly(&config.Settings{Repo: &config.RepoConfig{Description: config.Ptr(desc)}}), nil
	}
}

// postedBodies returns the bodies of every comment the bot created.
func (g *ghStub) postedBodies() []string {
	var out []string
	for _, r := range g.reqs {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/comments") {
			out = append(out, r.Body)
		}
	}
	return out
}

// recheck plans the pull request's head, not the default branch: the
// sticky comment must carry the change the pull request proposes.
func TestWebhook_IssueComment_RecheckDryRunsPRHead(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()
	var refs []string
	s := newServer(t, gh, settingsByRef(&refs))

	rr := postWebhook(s, "issue_comment",
		buildPayload("@repo-settings recheck", "NONE"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	if got := gh.count(http.MethodGet, "/repos/o/r/pulls/42"); got != 1 {
		t.Fatalf("pull request lookups = %d, want 1", got)
	}
	if len(refs) != 1 || refs[0] != "headsha" {
		t.Fatalf("settings refs = %q, want [headsha]", refs)
	}
	bodies := gh.postedBodies()
	if len(bodies) != 1 {
		t.Fatalf("sticky comments posted = %d, want 1", len(bodies))
	}
	if !strings.Contains(bodies[0], "from-head") || strings.Contains(bodies[0], "from-main") {
		t.Fatalf("sticky comment does not plan the PR head: %s", bodies[0])
	}
}

// A recheck whose pull request cannot be read stops before loading any
// settings: falling back to the default branch is the bug this replaces.
func TestWebhook_IssueComment_RecheckFailsWhenPRLookupFails(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()
	gh.pullStatus = http.StatusNotFound
	var refs []string
	s := newServer(t, gh, settingsByRef(&refs))

	rr := postWebhook(s, "issue_comment", buildPayload("@repo-settings recheck", "NONE"))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rr.Code, rr.Body.String())
	}
	if len(refs) != 0 {
		t.Fatalf("settings loaded at %q, want no load", refs)
	}
	bodies := gh.postedBodies()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "loading the pull request failed") {
		t.Fatalf("comments = %q, want one naming the failed lookup", bodies)
	}
}

// apply asks GitHub for the commenter's permission and ignores the
// payload's author_association, which calls read-only members MEMBER.
func TestWebhook_IssueComment_ApplyChecksRepositoryPermission(t *testing.T) {
	tests := []struct {
		name        string
		association string
		permission  string
		roleName    string
		wantApply   bool
		wantNamed   string
	}{
		{name: "read member is refused", association: "MEMBER", permission: "read", roleName: "read", wantNamed: "`read`"},
		{name: "triage collaborator is refused", association: "COLLABORATOR", permission: "read", roleName: "triage", wantNamed: "`triage`"},
		{name: "no role falls back to the base permission", association: "OWNER", permission: "none", wantNamed: "`none`"},
		{name: "write applies", association: "NONE", permission: "write", roleName: "write", wantApply: true},
		{name: "maintain applies", association: "NONE", permission: "write", roleName: "maintain", wantApply: true},
		{name: "admin applies", association: "NONE", permission: "admin", roleName: "admin", wantApply: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newGHStub(t)
			defer gh.close()
			gh.permission, gh.roleName = tt.permission, tt.roleName
			var refs []string
			s := newServer(t, gh, settingsByRef(&refs))

			rr := postWebhook(s, "issue_comment", buildPayload("@repo-settings apply", tt.association))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
			}
			if got := gh.count(http.MethodGet, "/repos/o/r/collaborators/alice/permission"); got != 1 {
				t.Fatalf("permission lookups = %d, want 1", got)
			}
			bodies := gh.postedBodies()
			if len(bodies) != 1 {
				t.Fatalf("comments posted = %d, want 1", len(bodies))
			}
			if !tt.wantApply {
				if len(refs) != 0 {
					t.Fatalf("settings loaded at %q despite refusal", refs)
				}
				if gh.count(http.MethodPatch, "/repos/o/r") != 0 {
					t.Fatal("repository was patched despite refusal")
				}
				if !strings.Contains(bodies[0], tt.wantNamed) {
					t.Fatalf("refusal %q does not name %s", bodies[0], tt.wantNamed)
				}
				return
			}
			// apply applies the default branch, never the PR head.
			if len(refs) != 1 || refs[0] != "" {
				t.Fatalf("settings refs = %q, want the default branch only", refs)
			}
			if gh.count(http.MethodPatch, "/repos/o/r") != 1 {
				t.Fatalf("repository patches = %d, want 1", gh.count(http.MethodPatch, "/repos/o/r"))
			}
			if !strings.Contains(bodies[0], "applied") {
				t.Fatalf("apply report missing: %s", bodies[0])
			}
		})
	}
}

// A permission lookup that fails refuses apply and logs GitHub's status;
// it never falls back to the payload's author_association.
func TestWebhook_IssueComment_ApplyRefusedWhenPermissionLookupFails(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{name: "user not found", status: http.StatusNotFound},
		{name: "GitHub error", status: http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := newGHStub(t)
			defer gh.close()
			gh.permStatus = tt.status
			var logs bytes.Buffer
			var refs []string
			s := newServerWithLogger(t, gh, settingsByRef(&refs), slog.New(slog.NewJSONHandler(&logs, nil)))

			rr := postWebhook(s, "issue_comment", buildPayload("@repo-settings apply", "OWNER"))
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", rr.Code, rr.Body.String())
			}
			if len(refs) != 0 {
				t.Fatalf("settings loaded at %q despite failed lookup", refs)
			}
			bodies := gh.postedBodies()
			if len(bodies) != 1 || !strings.Contains(bodies[0], "could not be checked") {
				t.Fatalf("comments = %q, want one refusal", bodies)
			}
			if want := fmt.Sprintf(`"status":%d`, tt.status); !strings.Contains(logs.String(), want) {
				t.Fatalf("log %q does not carry %s", logs.String(), want)
			}
		})
	}
}

// Bot-authored comments are skipped to break feedback loops.
func TestWebhook_IssueComment_IgnoresBotAuthor(t *testing.T) {
	gh := newGHStub(t)
	defer gh.close()
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Resolution, error) {
		return config.RepoOnly(nil), nil
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
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Resolution, error) {
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
	settings := func(_ context.Context, _ config.Repo, _ string) (*config.Resolution, error) {
		return config.RepoOnly(nil), nil
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
