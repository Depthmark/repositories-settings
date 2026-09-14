package ghapi

import (
	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
)

// Branch protection is where GitHub's read and write shapes differ most,
// and go-github models that split directly: Protection for what GET
// returns, ProtectionRequest for what PUT accepts. Same for the actor
// lists — BranchRestrictions carries user and team objects, while
// BranchRestrictionsRequest carries bare logins and slugs.
//
// Diffing the read shape against the desired document reported a change
// on every field of every run, so every configured branch was rewritten
// on every reconcile.

// DecodeProtection projects GET .../protection onto the shape
// branches.yml uses. Returns nil for an unprotected branch, which the
// differ reads as "create".
func DecodeProtection(p *github.Protection) map[string]any {
	if p == nil {
		return nil
	}
	// These arrive as {"enabled": bool} wrappers and are written as bare
	// booleans; a wrapper GitHub omits entirely means "off".
	out := map[string]any{
		"enforce_admins":                   p.EnforceAdmins != nil && p.EnforceAdmins.Enabled,
		"required_linear_history":          p.RequireLinearHistory != nil && p.RequireLinearHistory.Enabled,
		"allow_force_pushes":               p.AllowForcePushes != nil && p.AllowForcePushes.Enabled,
		"allow_deletions":                  p.AllowDeletions != nil && p.AllowDeletions.Enabled,
		"required_conversation_resolution": p.RequiredConversationResolution != nil && p.RequiredConversationResolution.Enabled,
	}
	if p.RequiredSignatures != nil {
		out["required_signatures"] = p.GetRequiredSignatures().GetEnabled()
	}

	// Only the fields we manage: GitHub also returns `url` and a
	// `checks` array restating `contexts`, neither of which is settable.
	if c := p.RequiredStatusChecks; c != nil {
		out["required_status_checks"] = map[string]any{
			"strict":   c.Strict,
			"contexts": toAny(derefStrings(c.Contexts)),
		}
	}

	if r := p.RequiredPullRequestReviews; r != nil {
		out["required_pull_request_reviews"] = map[string]any{
			"dismiss_stale_reviews":           r.DismissStaleReviews,
			"require_code_owner_reviews":      r.RequireCodeOwnerReviews,
			"require_last_push_approval":      r.RequireLastPushApproval,
			"required_approving_review_count": r.RequiredApprovingReviewCount,
		}
	}

	if r := p.Restrictions; r != nil {
		users := make([]any, 0, len(r.Users))
		for _, u := range r.Users {
			users = append(users, u.GetLogin())
		}
		teams := make([]any, 0, len(r.Teams))
		for _, t := range r.Teams {
			teams = append(teams, t.GetSlug())
		}
		apps := make([]any, 0, len(r.Apps))
		for _, a := range r.Apps {
			apps = append(apps, a.GetSlug())
		}
		out["restrictions"] = map[string]any{"users": users, "teams": teams, "apps": apps}
	}
	return out
}

// EncodeProtection builds the PUT body.
//
// This endpoint is a whole-object replace: the four sub-objects are
// required and each accepts null to mean "off", so a branch listed in
// branches.yml has its entire protection record stated here.
// config.Normalize has already filled the defaults, which is what lets
// the dry-run show exactly what this will write.
//
// required_signatures is deliberately absent — GitHub keeps it on its
// own sub-resource, and the lane applies it with a separate call.
func EncodeProtection(b config.BranchProtection) *github.ProtectionRequest {
	req := &github.ProtectionRequest{
		EnforceAdmins:                  deref(b.EnforceAdmins),
		RequireLinearHistory:           b.RequiredLinearHistory,
		AllowForcePushes:               b.AllowForcePushes,
		AllowDeletions:                 b.AllowDeletions,
		RequiredConversationResolution: b.RequiredConversationResolution,
	}
	if c := b.RequiredStatusChecks; c != nil {
		contexts := c.Contexts
		if contexts == nil {
			contexts = []string{}
		}
		req.RequiredStatusChecks = &github.RequiredStatusChecks{
			Strict:   deref(c.Strict),
			Contexts: &contexts,
		}
	}
	if r := b.RequiredPullRequestReviews; r != nil {
		req.RequiredPullRequestReviews = &github.PullRequestReviewsEnforcementRequest{
			DismissStaleReviews:          deref(r.DismissStaleReviews),
			RequireCodeOwnerReviews:      deref(r.RequireCodeOwnerReviews),
			RequiredApprovingReviewCount: derefInt(r.RequiredApprovingReviewCount),
			RequireLastPushApproval:      r.RequireLastPushApproval,
		}
	}
	if r := b.Restrictions; r != nil {
		req.Restrictions = &github.BranchRestrictionsRequest{
			Users: orEmpty(r.Users),
			Teams: orEmpty(r.Teams),
			Apps:  orEmpty(derefSlice(r.Apps)),
		}
	}
	return req
}

func derefStrings(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func derefSlice(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func deref(p *bool) bool {
	return p != nil && *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
