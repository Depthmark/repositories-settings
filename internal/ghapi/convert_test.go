package ghapi

import (
	"encoding/json"
	"testing"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
)

// marshalMap renders a request body the way it will go over the wire, so
// tests assert on what GitHub receives rather than on Go field values.
func marshalMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// A field the operator did not manage must not reach the wire at all.
// GitHub answers an explicit null with a 422, and on a PATCH it reads as
// "clear this field".
func TestEncodeHook_OmitsUnmanagedFields(t *testing.T) {
	body := marshalMap(t, EncodeHook(config.Webhook{URL: "https://h/x"}))
	cfg, _ := body["config"].(map[string]any)
	if cfg["url"] != "https://h/x" {
		t.Fatalf("delivery URL missing: %#v", body)
	}
	for _, k := range []string{"content_type", "insecure_ssl"} {
		if _, ok := cfg[k]; ok {
			t.Errorf("unmanaged config.%s should be omitted, got %#v", k, cfg[k])
		}
	}
	for _, k := range []string{"active", "events"} {
		if _, ok := body[k]; ok {
			t.Errorf("unmanaged %s should be omitted, got %#v", k, body[k])
		}
	}
}

// An explicit false is a managed value and must survive, including
// through GitHub's string spelling of insecure_ssl.
func TestEncodeHook_KeepsExplicitFalse(t *testing.T) {
	body := marshalMap(t, EncodeHook(config.Webhook{
		URL: "https://h/x", Active: config.Ptr(false), InsecureSSL: config.Ptr(false),
	}))
	if body["active"] != false {
		t.Errorf("explicit active=false lost: %#v", body["active"])
	}
	if got := body["config"].(map[string]any)["insecure_ssl"]; got != "0" {
		t.Errorf(`insecure_ssl should encode as "0", got %#v`, got)
	}
}

func TestEncodeHook_InsecureSSLTrueEncodesAsOne(t *testing.T) {
	body := marshalMap(t, EncodeHook(config.Webhook{URL: "https://h/x", InsecureSSL: config.Ptr(true)}))
	if got := body["config"].(map[string]any)["insecure_ssl"]; got != "1" {
		t.Errorf(`insecure_ssl should encode as "1", got %#v`, got)
	}
}

// Hook.URL is the API resource; Config.URL is where deliveries go. The
// config means the latter, and it is the key the diff matches on.
func TestDecodeHook_UsesDeliveryURLNotAPIURL(t *testing.T) {
	got := DecodeHook(&github.Hook{
		ID:  github.Ptr(int64(7)),
		URL: github.Ptr("https://api.github.com/repos/o/r/hooks/7"),
		Config: &github.HookConfig{
			URL: github.Ptr("https://h/x"), ContentType: github.Ptr("json"), InsecureSSL: github.Ptr("1"),
		},
		Active: github.Ptr(true),
		Events: []string{"push"},
	})
	if got["url"] != "https://h/x" {
		t.Errorf("expected the delivery URL, got %#v", got["url"])
	}
	if got["insecure_ssl"] != true {
		t.Errorf(`insecure_ssl "1" should decode to true, got %#v`, got["insecure_ssl"])
	}
	if got["_id"] != int64(7) {
		t.Errorf("hook id lost: %#v", got["_id"])
	}
}

func TestEncodeAutolink_KeepsExplicitFalse(t *testing.T) {
	body := marshalMap(t, EncodeAutolink(config.Autolink{
		KeyPrefix: "J-", URLTemplate: "https://j/<num>", IsAlphanumeric: config.Ptr(false),
	}))
	if body["is_alphanumeric"] != false {
		t.Fatalf("explicit is_alphanumeric=false lost: %#v", body)
	}
}

func TestEncodeAutolink_OmitsUnmanagedAlphanumeric(t *testing.T) {
	body := marshalMap(t, EncodeAutolink(config.Autolink{KeyPrefix: "J-", URLTemplate: "https://j/<num>"}))
	if _, ok := body["is_alphanumeric"]; ok {
		t.Fatalf("unmanaged is_alphanumeric should be omitted: %#v", body)
	}
}

// GitHub reports an environment's settings as a protection_rules array
// and accepts them as flat fields. Comparing the two shapes directly
// reported a change on every run.
func TestDecodeEnvironment_FlattensProtectionRules(t *testing.T) {
	got := DecodeEnvironment(&github.Environment{
		Name: github.Ptr("production"),
		ProtectionRules: []*github.ProtectionRule{
			{Type: github.Ptr("wait_timer"), WaitTimer: github.Ptr(30)},
			{
				Type:              github.Ptr("required_reviewers"),
				PreventSelfReview: github.Ptr(true),
				Reviewers: []*github.RequiredReviewer{
					{Type: github.Ptr("Team"), Reviewer: map[string]any{"id": float64(42), "slug": "core"}},
				},
			},
		},
		DeploymentBranchPolicy: &github.BranchPolicy{ProtectedBranches: github.Ptr(true)},
	})

	if got["name"] != "production" {
		t.Errorf("name lost: %#v", got["name"])
	}
	if got["wait_timer"] != 30 {
		t.Errorf("wait_timer should be flattened out of protection_rules, got %#v", got["wait_timer"])
	}
	if got["prevent_self_review"] != true {
		t.Errorf("prevent_self_review lost: %#v", got["prevent_self_review"])
	}
	reviewers, ok := got["reviewers"].([]any)
	if !ok || len(reviewers) != 1 {
		t.Fatalf("expected one reviewer, got %#v", got["reviewers"])
	}
	r := reviewers[0].(map[string]any)
	// The config spells reviewer ids as strings, so live state must too.
	if r["type"] != "Team" || r["id"] != "42" {
		t.Errorf("reviewer mismatch: %#v", r)
	}
	if _, ok := got["protection_rules"]; ok {
		t.Error("the raw protection_rules array must not reach the diff")
	}
}

// The environment name is the resource key and travels in the path;
// variables and secrets are not fields of this endpoint.
func TestEncodeEnvironment_OmitsNonBodyFields(t *testing.T) {
	body := marshalMap(t, EncodeEnvironment(config.Environment{
		Name:      "production",
		WaitTimer: config.Ptr(15),
		Variables: []config.EnvVariable{{Name: "A", Value: "b"}},
		Secrets:   []config.EnvSecretRef{{Name: "TOKEN"}},
	}))
	for _, k := range []string{"name", "variables", "secrets"} {
		if _, ok := body[k]; ok {
			t.Errorf("%q must not appear in the environment body: %#v", k, body)
		}
	}
	if body["wait_timer"] != float64(15) {
		t.Errorf("wait_timer lost: %#v", body["wait_timer"])
	}
}

func TestEncodeKey_OmitsUnmanagedReadOnly(t *testing.T) {
	body := marshalMap(t, EncodeKey(config.DeployKey{Title: "ci", Key: "ssh-ed25519 AAAA"}))
	if _, ok := body["read_only"]; ok {
		t.Error("unmanaged read_only should be omitted")
	}
	body = marshalMap(t, EncodeKey(config.DeployKey{Title: "ci", Key: "k", ReadOnly: config.Ptr(false)}))
	if body["read_only"] != false {
		t.Errorf("explicit read_only=false lost: %#v", body["read_only"])
	}
}

// GitHub never returns key material, so it can never compare equal and
// must stay out of the diff document.
func TestDecodeKey_ExcludesKeyMaterial(t *testing.T) {
	got := DecodeKey(&github.Key{
		ID: github.Ptr(int64(3)), Title: github.Ptr("ci"),
		Key: github.Ptr("ssh-ed25519 AAAA"), ReadOnly: github.Ptr(true),
	})
	if _, ok := got["key"]; ok {
		t.Errorf("key material leaked into the diff document: %#v", got)
	}
}

// role_name is authoritative; the boolean map is the fallback and is
// cumulative, so the most privileged flag wins.
func TestDecodeCollaborator_PrefersRoleName(t *testing.T) {
	got := DecodeCollaborator(&github.User{
		Login: github.Ptr("alice"), RoleName: github.Ptr("maintain"),
		Permissions: map[string]bool{"pull": true, "push": true},
	})
	if got["permission"] != "maintain" {
		t.Errorf("role_name should win, got %#v", got["permission"])
	}

	got = DecodeCollaborator(&github.User{
		Login:       github.Ptr("bob"),
		Permissions: map[string]bool{"pull": true, "push": true, "triage": true},
	})
	if got["permission"] != "push" {
		t.Errorf("expected the most privileged flag, got %#v", got["permission"])
	}
}

// Only two of the five security settings belong in security_and_analysis.
func TestEncodeSecurity_OnlyAnalysisFields(t *testing.T) {
	body := marshalMap(t, EncodeSecurity(map[string]bool{
		"secret_scanning":                 true,
		"secret_scanning_push_protection": false,
	}))
	saa := body["security_and_analysis"].(map[string]any)
	if saa["secret_scanning"].(map[string]any)["status"] != "enabled" {
		t.Errorf("secret_scanning not enabled: %#v", saa)
	}
	if saa["secret_scanning_push_protection"].(map[string]any)["status"] != "disabled" {
		t.Errorf("push protection not disabled: %#v", saa)
	}
	for _, k := range []string{"vulnerability_alerts", "automated_security_fixes", "private_vulnerability_reporting"} {
		if SecurityAndAnalysisFields[k] {
			t.Errorf("%q is its own endpoint and must not be routed through security_and_analysis", k)
		}
	}
}

// A repo PATCH carries only what differs: GitHub rejects some
// combinations a whole-object write would produce on every run.
func TestEncodeRepoPatch_OnlyChangedFields(t *testing.T) {
	body := marshalMap(t, EncodeRepoPatch(map[string]any{
		"has_issues":  false,
		"description": "hello",
	}))
	if body["has_issues"] != false || body["description"] != "hello" {
		t.Fatalf("changed fields lost: %#v", body)
	}
	if _, ok := body["private"]; ok {
		t.Errorf("unchanged fields must not be sent: %#v", body)
	}
}

// Actions settings are split across three endpoints; the field-group
// maps are what routes each change to the right one. A field in neither
// map falls through to the base permissions call, so a mistake here
// sends a setting to an endpoint that ignores it.
func TestActionsFieldGroups_AreDisjointAndComplete(t *testing.T) {
	base := []string{"enabled", "allowed_actions"}
	for _, f := range base {
		if ActionsAllowListFields[f] || ActionsWorkflowFields[f] {
			t.Errorf("%q belongs to the base permissions endpoint", f)
		}
	}
	for f := range ActionsAllowListFields {
		if ActionsWorkflowFields[f] {
			t.Errorf("%q is claimed by two endpoints", f)
		}
	}
	// Every field of ActionsConfig must be accounted for, or it silently
	// goes to the base endpoint and is dropped.
	all := map[string]bool{
		"enabled": true, "allowed_actions": true,
		"github_owned_allowed": true, "verified_allowed": true, "patterns_allowed": true,
		"default_workflow_permissions": true, "can_approve_pull_request_reviews": true,
	}
	body := marshalMap(t, config.ActionsConfig{
		Enabled: config.Ptr(true), AllowedActions: config.Ptr("selected"),
		GithubOwnedAllowed: config.Ptr(true), VerifiedAllowed: config.Ptr(true),
		PatternsAllowed:            []string{"acme/*"},
		DefaultWorkflowPermissions: config.Ptr("read"), CanApprovePullRequestReviews: config.Ptr(false),
	})
	for field := range body {
		if !all[field] {
			t.Errorf("ActionsConfig field %q is not routed to any endpoint", field)
		}
	}
}

func TestEncodeActionsAllowed_CarriesTheAllowList(t *testing.T) {
	got := EncodeActionsAllowed(config.ActionsConfig{
		GithubOwnedAllowed: config.Ptr(true),
		PatternsAllowed:    []string{"acme/*", "actions/checkout@*"},
	})
	if got.GetGithubOwnedAllowed() != true || len(got.PatternsAllowed) != 2 {
		t.Fatalf("allow-list lost: %#v", got)
	}
	if got.VerifiedAllowed != nil {
		t.Error("unmanaged verified_allowed should stay nil")
	}
}

// default_workflow_permissions decides whether GITHUB_TOKEN is read-only
// by default, so an explicit "read" must reach the wire intact.
func TestEncodeWorkflowDefaults_KeepsReadOnlyDefault(t *testing.T) {
	got := EncodeWorkflowDefaults(config.ActionsConfig{
		DefaultWorkflowPermissions:   config.Ptr("read"),
		CanApprovePullRequestReviews: config.Ptr(false),
	})
	if got.GetDefaultWorkflowPermissions() != "read" {
		t.Errorf("default_workflow_permissions lost: %#v", got)
	}
	if got.GetCanApprovePullRequestReviews() != false {
		t.Errorf("explicit false lost: %#v", got)
	}
}
