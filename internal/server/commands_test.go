package server

import (
	"testing"

	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

func TestParseComment_Variants(t *testing.T) {
	cases := []struct {
		name string
		body string
		slug string
		want Command
	}{
		{"empty body", "", "repo-settings", CmdNone},
		{"mention recheck", "@repo-settings recheck", "repo-settings", CmdRecheck},
		{"mention apply", "@repo-settings apply", "repo-settings", CmdApply},
		{"diff alias", "@repo-settings diff", "repo-settings", CmdRecheck},
		{"slash with slug", "/repo-settings recheck", "repo-settings", CmdRecheck},
		{"slash without configured slug", "/repo-settings recheck", "", CmdRecheck},
		{"colon separator", "@repo-settings: apply", "repo-settings", CmdApply},
		{"case insensitive", "@Repo-Settings APPLY", "repo-settings", CmdApply},
		{"trigger but no verb returns help", "@repo-settings", "repo-settings", CmdHelp},
		{"unrelated mention", "thanks @someone-else for the review", "repo-settings", CmdNone},
		{"quoted previous comment ignored", "> @repo-settings apply\n\nlooks fine to me", "repo-settings", CmdNone},
		{"first line wins", "thoughts?\n@repo-settings recheck", "repo-settings", CmdRecheck},
		{"help verb", "@repo-settings help", "repo-settings", CmdHelp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseComment(tc.body, tc.slug)
			if got.Command != tc.want {
				t.Fatalf("body=%q slug=%q -> %q, want %q", tc.body, tc.slug, got.Command, tc.want)
			}
		})
	}
}

// GitHub maps maintain to write and triage to read in `permission`, so
// the decision reads that field and role_name only names the role.
func TestCanApply(t *testing.T) {
	cases := []struct {
		name string
		perm ghclient.RepoPermission
		want bool
	}{
		{"admin", ghclient.RepoPermission{Permission: "admin", RoleName: "admin"}, true},
		{"maintain", ghclient.RepoPermission{Permission: "write", RoleName: "maintain"}, true},
		{"write", ghclient.RepoPermission{Permission: "write", RoleName: "write"}, true},
		{"custom role built on write", ghclient.RepoPermission{Permission: "write", RoleName: "release-manager"}, true},
		{"triage", ghclient.RepoPermission{Permission: "read", RoleName: "triage"}, false},
		{"read", ghclient.RepoPermission{Permission: "read", RoleName: "read"}, false},
		{"none", ghclient.RepoPermission{Permission: "none"}, false},
		{"role name alone grants nothing", ghclient.RepoPermission{RoleName: "admin"}, false},
		{"empty", ghclient.RepoPermission{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanApply(tc.perm); got != tc.want {
				t.Errorf("CanApply(%+v) = %v, want %v", tc.perm, got, tc.want)
			}
		})
	}
}

func TestApply_IsPrivileged(t *testing.T) {
	if !CmdApply.IsPrivileged() {
		t.Fatal("apply must be privileged")
	}
	if CmdRecheck.IsPrivileged() {
		t.Fatal("recheck should not be privileged")
	}
}

func TestHelpMarkdown_FallbackSlug(t *testing.T) {
	if got := HelpMarkdown(""); !contains(got, "repo-settings") {
		t.Fatalf("default slug missing in help markdown: %q", got)
	}
	if got := HelpMarkdown("custom"); !contains(got, "custom") {
		t.Fatalf("custom slug missing: %q", got)
	}
}

// avoid pulling strings just for one helper.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
