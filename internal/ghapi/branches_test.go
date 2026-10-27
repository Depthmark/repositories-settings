package ghapi

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
)

// The shape GitHub actually returns from
// GET /repos/{owner}/{repo}/branches/{branch}/protection.
const liveProtection = `{
  "url": "u",
  "required_status_checks": {"url":"u","strict":true,"contexts":["ci"],"checks":[{"context":"ci"}]},
  "enforce_admins": {"url":"u","enabled":true},
  "required_pull_request_reviews": {"url":"u","dismiss_stale_reviews":true,"require_code_owner_reviews":true,"required_approving_review_count":2},
  "restrictions": {"url":"u","users":[{"login":"alice","id":1}],"teams":[{"slug":"core","id":2}],"apps":[]},
  "required_linear_history": {"enabled": true},
  "allow_force_pushes": {"enabled": false},
  "allow_deletions": {"enabled": false},
  "required_conversation_resolution": {"enabled": true},
  "block_creations": {"enabled": true},
  "lock_branch": {"enabled": true},
  "allow_fork_syncing": {"enabled": false},
  "required_signatures": {"url":"u","enabled": false}
}`

// Decoding must land on the same shape the desired document uses, so a
// configuration matching reality compares equal. Before this projection
// existed, every one of these fields reported a change on every run.
func TestDecodeProtection_ProjectsOntoWriteShape(t *testing.T) {
	var p github.Protection
	if err := json.Unmarshal([]byte(liveProtection), &p); err != nil {
		t.Fatal(err)
	}
	got := DecodeProtection(&p)

	want := map[string]any{
		"enforce_admins":                   true,
		"required_linear_history":          true,
		"allow_force_pushes":               false,
		"allow_deletions":                  false,
		"required_conversation_resolution": true,
		"block_creations":                  true,
		"lock_branch":                      true,
		"allow_fork_syncing":               false,
		"required_signatures":              false,
		"required_status_checks": map[string]any{
			"strict":   true,
			"contexts": []any{"ci"},
		},
		"required_pull_request_reviews": map[string]any{
			"dismiss_stale_reviews":           true,
			"require_code_owner_reviews":      true,
			"require_last_push_approval":      false,
			"required_approving_review_count": 2,
		},
		"restrictions": map[string]any{
			"users": []any{"alice"},
			"teams": []any{"core"},
			"apps":  []any{},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded shape mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDecodeProtection_UnprotectedBranchIsNil(t *testing.T) {
	if got := DecodeProtection(nil); got != nil {
		t.Fatalf("expected nil for an unprotected branch, got %#v", got)
	}
}

// The branch travels in the path, and required_signatures is its own
// sub-resource — neither belongs in the protection body.
func TestEncodeProtection_ExcludesNonBodyFields(t *testing.T) {
	req := EncodeProtection(config.BranchProtection{
		Pattern:            "main",
		EnforceAdmins:      config.Ptr(true),
		RequiredSignatures: config.Ptr(true),
	})
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pattern", "required_signatures"} {
		if _, ok := body[k]; ok {
			t.Errorf("%q must not appear in the protection body: %s", k, raw)
		}
	}
	// GitHub requires all four sub-objects to be present, null meaning off.
	for _, k := range []string{
		"required_status_checks", "enforce_admins",
		"required_pull_request_reviews", "restrictions",
	} {
		if _, ok := body[k]; !ok {
			t.Errorf("required key %q missing from the PUT body: %s", k, raw)
		}
	}
}

// GitHub rejects null where it expects a list.
func TestEncodeProtection_NeverSendsNullLists(t *testing.T) {
	req := EncodeProtection(config.BranchProtection{
		Pattern:              "main",
		RequiredStatusChecks: &config.RequiredStatusChecks{},
		Restrictions:         &config.BranchRestrictions{},
	})
	if req.RequiredStatusChecks.Contexts == nil {
		t.Error("contexts must be an empty list, not null")
	}
	if req.Restrictions.Users == nil || req.Restrictions.Teams == nil || req.Restrictions.Apps == nil {
		t.Errorf("restriction lists must be empty lists, not null: %#v", req.Restrictions)
	}
}

// GitHub switches off any of these three that the PUT body leaves out,
// so the body states each one: the configured value, or the explicit
// false Normalize filled in.
func TestEncodeProtection_SendsLockAndCreationFields(t *testing.T) {
	s := &config.Settings{Branches: &config.BranchesConfig{Branches: []config.BranchProtection{
		{Pattern: "main", LockBranch: config.Ptr(true), BlockCreations: config.Ptr(true)},
	}}}
	s.Normalize()
	raw, err := json.Marshal(EncodeProtection(s.Branches.Branches[0]))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"lock_branch": true, "block_creations": true, "allow_fork_syncing": false}
	for k, v := range want {
		if got, ok := body[k]; !ok || got != v {
			t.Errorf("%s = %v (present: %v), want %v in %s", k, got, ok, v, raw)
		}
	}
}

// A sub-object GitHub omits decodes to an explicit null, the same value
// the desired document holds for one the config leaves out, so the two
// compare equal instead of reporting nil -> nil.
func TestDecodeProtection_AbsentSubObjectsAreNull(t *testing.T) {
	var p github.Protection
	if err := json.Unmarshal([]byte(`{"enforce_admins":{"enabled":false}}`), &p); err != nil {
		t.Fatal(err)
	}
	got := DecodeProtection(&p)
	for _, k := range []string{"required_status_checks", "required_pull_request_reviews", "restrictions"} {
		if v, ok := got[k]; !ok || v != nil {
			t.Errorf("%s = %v (present: %v), want an explicit nil", k, v, ok)
		}
	}
}
