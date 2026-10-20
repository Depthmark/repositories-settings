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
