package config

import (
	"context"
	"errors"
	"testing"
)

type memFetcher map[string][]byte

func (m memFetcher) GetFile(_ context.Context, _ Repo, path, _ string) ([]byte, error) {
	if b, ok := m[path]; ok {
		return b, nil
	}
	return nil, ErrNotFound
}

func TestLoad_HappyPath(t *testing.T) {
	f := memFetcher{
		".github/settings/repo.yml": []byte(`_version: 1
repository:
  description: hello
  has_issues: true
topics:
  - infra
  - go
`),
		".github/settings/teams.yml": []byte(`_version: 1
teams:
  - slug: platform
    permission: push
`),
	}
	s, err := Load(context.Background(), f, Repo{Owner: "o", Name: "r"}, "main")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.Repo == nil || s.Repo.Description == nil || *s.Repo.Description != "hello" {
		t.Fatalf("repo not loaded: %+v", s.Repo)
	}
	if s.Topics == nil || (*s.Topics)[1] != "go" {
		t.Fatalf("topics: %+v", s.Topics)
	}
	if s.Teams == nil || s.Teams.Teams[0].Slug != "platform" {
		t.Fatalf("teams: %+v", s.Teams)
	}
}

func TestLoad_VersionMismatch(t *testing.T) {
	f := memFetcher{".github/settings/repo.yml": []byte("_version: 2\n")}
	_, err := Load(context.Background(), f, Repo{}, "")
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationError, got %v", err)
	}
}

func TestAffectedKeys(t *testing.T) {
	tests := []struct {
		in     []string
		want   []string
		nilSet bool
	}{
		{[]string{".github/settings/teams.yml"}, []string{"teams"}, false},
		{[]string{".github/settings/repo.yml"}, []string{"repo", "topics"}, false},
		{[]string{}, nil, true},
		{[]string{".github/settings/unknown.yml"}, nil, true},
		{[]string{"teams.yml", "rulesets.yml"}, []string{"teams", "rulesets"}, false},
	}
	for _, tc := range tests {
		got := AffectedKeys(tc.in)
		if tc.nilSet {
			if got != nil {
				t.Errorf("AffectedKeys(%v) = %v, want nil", tc.in, got)
			}
			continue
		}
		for _, w := range tc.want {
			if _, ok := got[w]; !ok {
				t.Errorf("AffectedKeys(%v) missing %q", tc.in, w)
			}
		}
	}
}

func TestResolve_Provenance(t *testing.T) {
	org := &Settings{Repo: &RepoConfig{HasIssues: ptrB(true)}}
	repo := &Settings{Repo: &RepoConfig{HasWiki: ptrB(false)}}
	out, prov := Resolve(
		Layer{Source: "org", Settings: org},
		Layer{Source: "repo", Settings: repo},
	)
	if prov["repo"] != "repo" {
		t.Fatalf("prov[repo] = %q want repo", prov["repo"])
	}
	if out.Repo == nil || out.Repo.HasWiki == nil || *out.Repo.HasWiki {
		t.Fatalf("repo overlay didn't win: %+v", out.Repo)
	}
	if out.Repo.HasIssues != nil {
		t.Fatalf("org repo wholly replaced — but its HasIssues field shouldn't carry over since repo wholly replaces the resource")
	}
}

func TestPolicy_RepoLocked(t *testing.T) {
	resolved := &Settings{Repo: &RepoConfig{Description: ptrS("x")}}
	repoCfg := &Settings{Repo: &RepoConfig{Description: ptrS("x")}}
	p := AdminPolicy{Repo: &PolicyRepo{Locked: []string{"description"}}}
	v := ValidatePolicy(resolved, repoCfg, p)
	if len(v) == 0 || v[0].OrgPolicy != "repo.locked" {
		t.Fatalf("got %+v", v)
	}
}

func TestPolicy_MaxPermission(t *testing.T) {
	resolved := &Settings{Teams: &TeamsConfig{Teams: []TeamAccess{{Slug: "x", Permission: PermAdmin}}}}
	maxPerm := PermPush
	p := AdminPolicy{Teams: &PolicyTeams{MaxPermission: &maxPerm}}
	v := ValidatePolicy(resolved, &Settings{Teams: resolved.Teams}, p)
	if len(v) != 1 || v[0].OrgPolicy != "teams.max_permission" {
		t.Fatalf("got %+v", v)
	}
}

func TestSuborgMatch(t *testing.T) {
	m := SuborgMatch{Repos: []string{"infra-*"}, Teams: []string{"platform"}}
	if !m.Matches(RepoContext{Repo: Repo{Owner: "o", Name: "infra-prod"}, Teams: []string{"platform"}}) {
		t.Fatal("should match")
	}
	if m.Matches(RepoContext{Repo: Repo{Owner: "o", Name: "infra-prod"}, Teams: []string{"other"}}) {
		t.Fatal("teams mismatch should fail")
	}
	if m.Matches(RepoContext{Repo: Repo{Owner: "o", Name: "frontend"}, Teams: []string{"platform"}}) {
		t.Fatal("repo mismatch should fail")
	}
}

func ptrB(b bool) *bool     { return &b }
func ptrS(s string) *string { return &s }
