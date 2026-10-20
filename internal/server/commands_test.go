package server

import "testing"

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

func TestIsPrivilegedAuthor(t *testing.T) {
	cases := []struct {
		assoc string
		want  bool
	}{
		{"OWNER", true},
		{"MEMBER", true},
		{"COLLABORATOR", true},
		{"owner", true}, // case-insensitive
		{"CONTRIBUTOR", false},
		{"FIRST_TIME_CONTRIBUTOR", false},
		{"NONE", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsPrivilegedAuthor(tc.assoc); got != tc.want {
			t.Errorf("IsPrivilegedAuthor(%q) = %v, want %v", tc.assoc, got, tc.want)
		}
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
