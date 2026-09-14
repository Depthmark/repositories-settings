package reconciler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/logger"
)

// countingGitHub fails the test if it is contacted at all: a policy
// refusal has to happen before any live state is read.
func countingGitHub(t *testing.T, hits *int32) *ghclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	rl := ghclient.NewRateLimiter(logger.Discard(), ghclient.Options{Concurrency: 4})
	t.Cleanup(rl.Stop)
	return ghclient.New(ghclient.Config{
		APIURL: srv.URL, Limiter: rl, HTTPClient: srv.Client(), Logger: logger.Discard(),
	})
}

func lockedVisibilityResolution() *config.Resolution {
	vis := "public"
	repoLayer := &config.Settings{Repo: &config.RepoConfig{Visibility: &vis}}
	return config.ResolveFor(
		&config.AdminLayer{Policy: config.AdminPolicy{
			Repo: &config.PolicyRepo{Locked: []string{"visibility"}},
		}},
		repoLayer,
		config.RepoContext{Repo: config.Repo{Owner: "o", Name: "r"}},
	)
}

// An error-severity violation stops the whole run. A partial apply of
// the parts that happened to be legal is the outcome policy exists to
// prevent: a repository that unlocks its visibility should not also get
// its team grants written.
func TestReconcile_PolicyErrorBlocksBeforeAnyCall(t *testing.T) {
	var hits int32
	cl := countingGitHub(t, &hits)

	rep, err := New(logger.Discard()).Reconcile(
		context.Background(), cl, config.Repo{Owner: "o", Name: "r"},
		lockedVisibilityResolution(), config.TriggerPush, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Blocked {
		t.Fatal("a locked-field override must block the reconcile")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("policy refusal must precede every GitHub call, got %d", n)
	}
	if len(rep.Diffs) != 0 {
		t.Errorf("a refused run computes no diff, got %+v", rep.Diffs)
	}
	if len(rep.Violations) == 0 {
		t.Error("the report must carry the violations that caused the refusal")
	}
	// The synthetic failure is what makes /api/check conclude "failure";
	// without it a blocked run would report success to the workflow.
	failed := false
	for _, r := range rep.Applied {
		if !r.Success {
			failed = true
		}
	}
	if !failed {
		t.Error("a blocked run must surface as a failed result to API consumers")
	}
}

// A dry run reports the refusal too, so a PR check tells the
// contributor what an apply would reject rather than approving it.
func TestReconcile_PolicyBlocksDryRunToo(t *testing.T) {
	var hits int32
	rep, err := New(logger.Discard()).Reconcile(
		context.Background(), countingGitHub(t, &hits), config.Repo{Owner: "o", Name: "r"},
		lockedVisibilityResolution(), config.TriggerWebhook, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Blocked {
		t.Fatal("dry runs must report the refusal, not hide it")
	}

	out := FormatReportMarkdown(rep)
	for _, want := range []string{
		"refused by org policy",
		"org policy violation",
		"`repository.visibility`",
		"`repo.locked`",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Comment `apply`") {
		t.Error("the apply hint must not appear on a refused run")
	}
}

// A warning announces a policy the org has not started enforcing. It
// belongs in the report, but it must not stop the apply.
func TestReconcile_PolicyWarningDoesNotBlock(t *testing.T) {
	var hits int32
	res := config.RepoOnly(&config.Settings{})
	res.Violations = []config.PolicyViolation{{
		Field: "repository.has_wiki", Message: "wikis will be locked next quarter",
		Severity: config.SevWarning, OrgPolicy: "repo.locked",
	}}

	rep, err := New(logger.Discard()).Reconcile(
		context.Background(), countingGitHub(t, &hits), config.Repo{Owner: "o", Name: "r"},
		res, config.TriggerManual, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked {
		t.Fatal("a warning must not block the apply")
	}
	out := FormatReportMarkdown(rep)
	if !strings.Contains(out, "org policy warning") {
		t.Errorf("the warning should still be reported:\n%s", out)
	}
}
