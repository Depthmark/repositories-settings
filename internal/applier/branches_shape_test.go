package applier

import (
	"encoding/json"
	"testing"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
)

// End-to-end shape check: a branches.yml that describes exactly what
// GitHub already has must reconcile to a noop. It used to report five
// changes on every run — enforce_admins, required_linear_history,
// required_conversation_resolution and both restriction lists — because
// the raw GET response was compared against the desired document.
func TestBranchesLane_MatchingConfigIsNoop(t *testing.T) {
	const live = `{
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
	var raw github.Protection
	if err := json.Unmarshal([]byte(live), &raw); err != nil {
		t.Fatal(err)
	}

	s := &config.Settings{Branches: &config.BranchesConfig{Branches: []config.BranchProtection{{
		Pattern:                        "main",
		RequiredStatusChecks:           &config.RequiredStatusChecks{Strict: config.Ptr(true), Contexts: []string{"ci"}},
		EnforceAdmins:                  config.Ptr(true),
		RequiredLinearHistory:          config.Ptr(true),
		RequiredConversationResolution: config.Ptr(true),
		RequiredPullRequestReviews: &config.RequiredPRReviews{
			RequiredApprovingReviewCount: config.Ptr(2),
			DismissStaleReviews:          config.Ptr(true),
			RequireCodeOwnerReviews:      config.Ptr(true),
		},
		Restrictions: &config.BranchRestrictions{Users: []string{"alice"}, Teams: []string{"core"}},
	}}}}
	s.Normalize()

	desired, err := diff.ToMap(s.Branches.Branches[0])
	if err != nil {
		t.Fatal(err)
	}
	delete(desired, "pattern")

	d := diff.SingleResource("branches.main", ghapi.DecodeProtection(&raw), desired)
	if d.Action != diff.Noop {
		for _, c := range d.Changes {
			t.Errorf("spurious change %s: %v -> %v", c.Path, c.From, c.To)
		}
		t.Fatalf("expected noop, got %s", d.Action)
	}
}

// Normalize completes the record because PUT .../protection replaces
// the whole object: anything left out is switched off, and the dry-run
// has to say so.
func TestNormalize_CompletesBranchProtectionRecord(t *testing.T) {
	s := &config.Settings{Branches: &config.BranchesConfig{Branches: []config.BranchProtection{{
		Pattern:              "main",
		RequiredStatusChecks: &config.RequiredStatusChecks{Contexts: []string{"ci"}},
	}}}}
	s.Normalize()
	b := s.Branches.Branches[0]
	if b.EnforceAdmins == nil || *b.EnforceAdmins {
		t.Errorf("enforce_admins should default to an explicit false, got %v", b.EnforceAdmins)
	}
	if b.RequiredStatusChecks.Strict == nil {
		t.Error("strict should be filled in once required_status_checks is configured")
	}
	// Sub-objects the operator never mentioned stay off (null on PUT).
	if b.Restrictions != nil {
		t.Error("restrictions must stay unset rather than being invented")
	}
}
