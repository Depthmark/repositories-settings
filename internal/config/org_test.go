package config

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// mapFetcher serves files from an in-memory "repo/path" map.
type mapFetcher struct {
	files         map[string]string
	repositoryErr error
	// reads counts GetFile calls, so the admin cache's reuse is testable.
	// Settings files are fetched in parallel, hence the atomic.
	reads atomic.Int32
}

func (m *mapFetcher) CheckRepository(context.Context, Repo) error {
	return m.repositoryErr
}

func (m *mapFetcher) GetFile(_ context.Context, repo Repo, path, _ string) ([]byte, error) {
	m.reads.Add(1)
	if v, ok := m.files[repo.String()+"/"+path]; ok {
		return []byte(v), nil
	}
	return nil, ErrNotFound
}

func TestLoadAdmin_EmptyRepoIsNotAnError(t *testing.T) {
	layer, err := LoadAdmin(context.Background(), &mapFetcher{files: map[string]string{}},
		Repo{Owner: "acme", Name: ".github"}, "")
	if err != nil {
		t.Fatalf("an org with no admin files must resolve, got %v", err)
	}
	if layer.Policy != (AdminPolicy{}) || len(layer.Suborgs) != 0 {
		t.Fatalf("expected an empty layer, got %+v", layer)
	}
}

func TestLoadAdmin_InaccessibleRepositoryFailsClosed(t *testing.T) {
	f := &mapFetcher{repositoryErr: ErrNotFound}
	layer, err := LoadAdmin(context.Background(), f, Repo{Owner: "acme", Name: ".github"}, "")
	if !errors.Is(err, ErrNotFound) || layer != nil {
		t.Fatalf("inaccessible configured repository must fail, got layer=%+v err=%v", layer, err)
	}
	if f.reads.Load() != 0 {
		t.Fatal("optional files must not be loaded before repository access succeeds")
	}
}

func TestValidateAdminRepoRef(t *testing.T) {
	for _, ref := range []string{"", ".github", "acme/.github"} {
		if err := ValidateAdminRepoRef(ref); err != nil {
			t.Errorf("valid reference %q: %v", ref, err)
		}
	}
	for _, ref := range []string{" ", "acme/", "/.github", "acme/../other", "../other", "acme/%2e", "acme/name?ref=evil"} {
		if err := ValidateAdminRepoRef(ref); err == nil {
			t.Errorf("invalid reference %q accepted", ref)
		}
	}
}

func TestLoadAdmin_ReadsSettingsPolicyAndSuborgs(t *testing.T) {
	f := &mapFetcher{files: map[string]string{
		"acme/.github/.github/settings/repo.yml": "_version: 1\nrepository:\n  has_wiki: false\n",
		"acme/.github/.github/settings/policy.yml": `_version: 1
policy:
  repo:
    locked: [visibility]
`,
		"acme/.github/.github/settings/suborgs.yml": `_version: 1
suborgs:
  - name: platform
    match:
      repos: ["platform-*"]
`,
		"acme/.github/.github/settings/suborgs/platform/repo.yml": "_version: 1\nrepository:\n  has_issues: false\n",
	}}

	layer, err := LoadAdmin(context.Background(), f, Repo{Owner: "acme", Name: ".github"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if layer.Settings == nil || layer.Settings.Repo == nil || layer.Settings.Repo.HasWiki == nil || *layer.Settings.Repo.HasWiki {
		t.Fatalf("org defaults not loaded: %+v", layer.Settings)
	}
	if layer.Policy.Repo == nil || len(layer.Policy.Repo.Locked) != 1 {
		t.Fatalf("policy not loaded: %+v", layer.Policy)
	}
	if len(layer.Suborgs) != 1 || layer.Suborgs[0].Name != "platform" {
		t.Fatalf("suborg index not loaded: %+v", layer.Suborgs)
	}
	so := layer.Suborgs[0]
	if so.Settings == nil || so.Settings.Repo == nil || so.Settings.Repo.HasIssues == nil {
		t.Fatalf("suborg settings not loaded: %+v", so.Settings)
	}
}

// A suborg name becomes a path segment. A traversal in it would read
// files from outside the suborg tree — including the repository's own.
func TestLoadAdmin_RejectsSuborgNameTraversal(t *testing.T) {
	for _, name := range []string{"../evil", "a/b", ".."} {
		f := &mapFetcher{files: map[string]string{
			"acme/.github/.github/settings/suborgs.yml": "_version: 1\nsuborgs:\n  - name: \"" + name + "\"\n    match:\n      repos: [\"*\"]\n",
		}}
		_, err := LoadAdmin(context.Background(), f, Repo{Owner: "acme", Name: ".github"}, "")
		if err == nil {
			t.Errorf("suborg name %q should be rejected", name)
			continue
		}
		var ve *ValidationError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), "single path segment") {
			t.Errorf("suborg name %q: want a path-segment validation error, got %v", name, err)
		}
	}
}

func TestResolveFor_LayersAndValidates(t *testing.T) {
	orgWiki, repoWiki := false, true
	admin := &AdminLayer{
		Settings: &Settings{Repo: &RepoConfig{HasWiki: &orgWiki}},
		Policy:   AdminPolicy{Repo: &PolicyRepo{Locked: []string{"has_wiki"}}},
	}

	t.Run("repo override of a locked field is an error", func(t *testing.T) {
		repoLayer := &Settings{Repo: &RepoConfig{HasWiki: &repoWiki}}
		res := ResolveFor(admin, repoLayer, RepoContext{Repo: Repo{Owner: "acme", Name: "app"}})
		if len(res.Blocking()) != 1 {
			t.Fatalf("expected one blocking violation, got %+v", res.Violations)
		}
		if res.Provenance["repo"] != "repo" {
			t.Errorf("repo layer should win the merge, provenance = %+v", res.Provenance)
		}
	})

	t.Run("inheriting the locked field is allowed", func(t *testing.T) {
		res := ResolveFor(admin, &Settings{}, RepoContext{Repo: Repo{Owner: "acme", Name: "app"}})
		if len(res.Blocking()) != 0 {
			t.Fatalf("inheriting a locked field is not an override, got %+v", res.Violations)
		}
		if res.Settings.Repo == nil || *res.Settings.Repo.HasWiki {
			t.Errorf("org default should survive, got %+v", res.Settings.Repo)
		}
	})

	t.Run("suborg applies only to matching repos", func(t *testing.T) {
		issues := false
		withSuborg := *admin
		withSuborg.Suborgs = []Suborg{{
			Name:     "platform",
			Match:    SuborgMatch{Repos: []string{"platform-*"}},
			Settings: &Settings{Repo: &RepoConfig{HasIssues: &issues}},
		}}
		matched := ResolveFor(&withSuborg, &Settings{}, RepoContext{Repo: Repo{Owner: "acme", Name: "platform-api"}})
		if matched.Provenance["repo"] != "suborg:platform" {
			t.Errorf("matching repo should take the suborg layer, got %+v", matched.Provenance)
		}
		other := ResolveFor(&withSuborg, &Settings{}, RepoContext{Repo: Repo{Owner: "acme", Name: "web"}})
		if other.Provenance["repo"] != "org" {
			t.Errorf("non-matching repo should stay on the org layer, got %+v", other.Provenance)
		}
	})
}

// Selective reconcile narrows what gets applied. It must not narrow what
// policy refused, or a push touching one unrelated file would let a
// locked override through.
func TestResolution_FilteringKeepsTheVerdict(t *testing.T) {
	res := &Resolution{
		Settings:   &Settings{Repo: &RepoConfig{}, Teams: &TeamsConfig{}},
		Violations: []PolicyViolation{{Field: "repository.visibility", Severity: SevError}},
	}
	narrowed := res.Filtering(map[string]struct{}{"teams": {}})
	if len(narrowed.Blocking()) != 1 {
		t.Fatalf("filtering dropped the policy verdict: %+v", narrowed.Violations)
	}
	if narrowed.Settings.Repo != nil {
		t.Error("repo settings should have been filtered out")
	}
	if res.Settings.Repo == nil {
		t.Error("Filtering must not mutate the original resolution")
	}
}

func TestAdminCache(t *testing.T) {
	files := map[string]string{
		"acme/.github/.github/settings/policy.yml": "_version: 1\npolicy:\n  repo:\n    locked: [visibility]\n",
	}
	ctx := context.Background()

	t.Run("disabled when unconfigured", func(t *testing.T) {
		layer, err := NewAdminCache(&mapFetcher{files: files}, "", nil).For(ctx, Repo{Owner: "acme", Name: "app"})
		if err != nil || layer != nil {
			t.Fatalf("no ORG_ADMIN_REPO means no org layer, got %+v %v", layer, err)
		}
	})

	t.Run("bare name resolves in the target owner", func(t *testing.T) {
		f := &mapFetcher{files: files}
		c := NewAdminCache(f, ".github", nil)
		layer, err := c.For(ctx, Repo{Owner: "acme", Name: "app"})
		if err != nil {
			t.Fatal(err)
		}
		if layer == nil || layer.Policy.Repo == nil {
			t.Fatalf("expected acme's policy, got %+v", layer)
		}
		reads := f.reads.Load()
		if _, err := c.For(ctx, Repo{Owner: "acme", Name: "other"}); err != nil {
			t.Fatal(err)
		}
		if got := f.reads.Load(); got != reads {
			t.Errorf("second repo in the same org re-read the admin repo: %d extra reads", got-reads)
		}
	})

	t.Run("the admin repo does not govern itself", func(t *testing.T) {
		c := NewAdminCache(&mapFetcher{files: files}, ".github", nil)
		layer, err := c.For(ctx, Repo{Owner: "acme", Name: ".github"})
		if err != nil {
			t.Fatal(err)
		}
		if layer != nil {
			t.Error("the admin repository must not apply its own policy to itself")
		}
	})
}

// A failed read of the admin repository must not be cached. The caller
// fails closed on the error, so caching it would refuse every reconcile
// in the org until the TTL expired.
func TestAdminCache_DoesNotCacheFailures(t *testing.T) {
	f := &failingFetcher{}
	c := NewAdminCache(f, ".github", nil)

	if _, err := c.For(context.Background(), Repo{Owner: "acme", Name: "app"}); err == nil {
		t.Fatal("expected the read failure to surface")
	}
	f.recovered.Store(true)
	layer, err := c.For(context.Background(), Repo{Owner: "acme", Name: "app"})
	if err != nil {
		t.Fatalf("the next call should retry, got %v", err)
	}
	if layer == nil || layer.Policy.Repo == nil {
		t.Fatalf("expected the recovered policy, got %+v", layer)
	}
}

// failingFetcher errors until recovered is set, then serves a policy.
type failingFetcher struct{ recovered atomic.Bool }

func (f *failingFetcher) CheckRepository(context.Context, Repo) error { return nil }

func (f *failingFetcher) GetFile(_ context.Context, _ Repo, path, _ string) ([]byte, error) {
	if !f.recovered.Load() {
		return nil, errors.New("upstream unavailable")
	}
	if path == AdminPolicyPath {
		return []byte("_version: 1\npolicy:\n  repo:\n    locked: [visibility]\n"), nil
	}
	return nil, ErrNotFound
}
