package oidc

import (
	"strings"
	"testing"
)

// githubClaims is a well-formed GitHub.com Actions claim set that tests
// override one field at a time.
func githubClaims(overrides map[string]any) Claims {
	c := Claims{
		"iss":                 GitHubActionsIssuer,
		"sub":                 "repo:acme/app:ref:refs/heads/main",
		"repository":          "acme/app",
		"repository_owner":    "acme",
		"repository_id":       "42",
		"repository_owner_id": "7",
	}
	for k, v := range overrides {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

func TestParseGitHubIdentity_OtherIssuersAreNotOurs(t *testing.T) {
	id, err := ParseGitHubIdentity(Claims{"iss": "https://accounts.google.com"}, nil, false)
	if err != nil {
		t.Fatalf("a non-GitHub issuer is not an error here, got %v", err)
	}
	if id != nil {
		t.Fatalf("expected no GitHub identity, got %+v", id)
	}
}

func TestParseGitHubIdentity_Accepts(t *testing.T) {
	t.Run("the legacy subject form", func(t *testing.T) {
		id, err := ParseGitHubIdentity(githubClaims(nil), nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if id.Repository != "acme/app" {
			t.Fatalf("Repository = %q, want acme/app", id.Repository)
		}
		if id.ImmutableSubject {
			t.Error("the legacy subject carries no IDs")
		}
	})

	t.Run("the immutable subject form", func(t *testing.T) {
		id, err := ParseGitHubIdentity(githubClaims(map[string]any{
			"sub": "repo:acme@7/app@42:ref:refs/heads/main",
		}), nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if !id.ImmutableSubject {
			t.Error("the subject carried IDs and should be marked immutable")
		}
		if id.Repository != "acme/app" {
			t.Fatalf("Repository = %q, want acme/app", id.Repository)
		}
	})

	t.Run("owner case does not have to match", func(t *testing.T) {
		if _, err := ParseGitHubIdentity(githubClaims(map[string]any{
			"repository_owner": "ACME",
		}), nil, false); err != nil {
			t.Fatalf("GitHub logins are case-insensitive: %v", err)
		}
	})
}

func TestParseGitHubIdentity_Rejects(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]any
		immutable bool
		want      string
	}{
		{
			name:      "a subject naming a different repository",
			overrides: map[string]any{"sub": "repo:acme/other:ref:refs/heads/main"},
			want:      "sub",
		},
		{
			// The signed repository claim and the subject must agree, or
			// a templated subject could speak for someone else.
			name:      "a subject naming a second repository",
			overrides: map[string]any{"sub": "repo:acme/app:x:repo:acme/app:y"},
			want:      "more than once",
		},
		{
			name:      "a subject with no repo segment",
			overrides: map[string]any{"sub": "workflow_ref:acme/app/.github/workflows/x.yml@refs/heads/main"},
			want:      "no repo: segment",
		},
		{
			name:      "an owner claim that disagrees with the repository claim",
			overrides: map[string]any{"repository_owner": "someone-else"},
			want:      "repository_owner",
		},
		{
			name:      "a repository claim that is not owner/name",
			overrides: map[string]any{"repository": "acme"},
			want:      "repository",
		},
		{
			name:      "a missing repository_id",
			overrides: map[string]any{"repository_id": nil},
			want:      "repository_id",
		},
		{
			name:      "a non-numeric repository_id",
			overrides: map[string]any{"repository_id": "42abc"},
			want:      "repository_id",
		},
		{
			name:      "a zero repository_owner_id",
			overrides: map[string]any{"repository_owner_id": "0"},
			want:      "repository_owner_id",
		},
		{
			name:      "a claim of the wrong type",
			overrides: map[string]any{"repository": 42},
			want:      "not a string",
		},
		{
			name:      "an immutable subject whose IDs do not match the signed claims",
			overrides: map[string]any{"sub": "repo:acme@999/app@999:ref:refs/heads/main"},
			want:      "sub",
		},
		{
			name:      "a legacy subject when immutable subjects are required",
			overrides: nil,
			immutable: true,
			want:      "immutable owner and repository IDs",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, err := ParseGitHubIdentity(githubClaims(tc.overrides), nil, tc.immutable)
			if err == nil {
				t.Fatalf("expected a rejection, got %+v", id)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error mentioning %q, got %v", tc.want, err)
			}
			// The message names the claim, never its value: it is
			// returned to a caller that may not be the repository owner.
			if strings.Contains(err.Error(), "acme/other") {
				t.Errorf("the error echoed a claim value: %v", err)
			}
		})
	}
}

// A subject template can legitimately place a repo: segment anywhere,
// including one whose value is the literal string "repo".
func TestSubjectRepoSegments(t *testing.T) {
	for _, tc := range []struct {
		subject string
		want    []string
	}{
		{"repo:acme/app:ref:refs/heads/main", []string{"acme/app"}},
		{"job_workflow_ref:x:repo:acme/app", []string{"acme/app"}},
		{"repo:acme/app:environment:repo:other/thing", []string{"acme/app", "other/thing"}},
		{"repo:repo:repo:acme/app", []string{"repo", "repo", "acme/app"}},
		{"environment:production", nil},
	} {
		t.Run(tc.subject, func(t *testing.T) {
			got := subjectRepoSegments(tc.subject)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}
