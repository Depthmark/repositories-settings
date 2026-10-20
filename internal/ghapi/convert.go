package ghapi

import (
	"strconv"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
)

// This file is the typed boundary between our configuration model and
// go-github's request/response types. Every resource gets two functions:
//
//	Decode<X>  live SDK type  → the desired-document shape
//	Encode<X>  our config     → the SDK's write type
//
// They exist because "what GitHub reports" and "what GitHub accepts" are
// not the same document, and neither is the same as what the operator
// wrote. Keeping all three conversions in one place — rather than open
// coded inside each lane's closure — is what makes them testable without
// an HTTP server, and what stopped three separate lanes from silently
// reporting a change on every reconcile.
//
// Decoders produce the same key set that json.Marshal of the matching
// config type produces, because that is what the differ compares against.

// ---------- webhooks ----------

// DecodeHook flattens a hook into the shape webhooks.yml uses.
//
// Two different URLs live on a hook: Hook.URL is the API resource, and
// Hook.Config.URL is where deliveries go. The config means the latter,
// and it is the key the diff matches on.
func DecodeHook(h *github.Hook) map[string]any {
	cfg := h.GetConfig()
	return map[string]any{
		"_id":          h.GetID(),
		"url":          cfg.GetURL(),
		"content_type": cfg.GetContentType(),
		"insecure_ssl": cfg.GetInsecureSSL() == "1",
		"active":       h.GetActive(),
		"events":       toAny(h.Events),
	}
}

// EncodeHook builds a create/update body. Unmanaged fields are left nil
// so the SDK omits them, which is how GitHub's own defaults still apply.
func EncodeHook(w config.Webhook) *github.Hook {
	h := &github.Hook{
		Name:   github.Ptr("web"),
		Config: &github.HookConfig{URL: github.Ptr(w.URL)},
		Active: w.Active,
	}
	if w.ContentType != nil {
		h.Config.ContentType = w.ContentType
	}
	if w.InsecureSSL != nil {
		// GitHub represents this one as the strings "0" and "1".
		h.Config.InsecureSSL = github.Ptr(map[bool]string{true: "1", false: "0"}[*w.InsecureSSL])
	}
	if w.Events != nil {
		h.Events = w.Events
	}
	return h
}

// ---------- autolinks ----------

func DecodeAutolink(a *github.Autolink) map[string]any {
	return map[string]any{
		"_id":             a.GetID(),
		"key_prefix":      a.GetKeyPrefix(),
		"url_template":    a.GetURLTemplate(),
		"is_alphanumeric": a.GetIsAlphanumeric(),
	}
}

func EncodeAutolink(a config.Autolink) *github.AutolinkOptions {
	return &github.AutolinkOptions{
		KeyPrefix:      github.Ptr(a.KeyPrefix),
		URLTemplate:    github.Ptr(a.URLTemplate),
		IsAlphanumeric: a.IsAlphanumeric,
	}
}

// ---------- deploy keys ----------

// DecodeKey omits the key material: GitHub never returns it, so a
// desired document containing it could never compare equal.
func DecodeKey(k *github.Key) map[string]any {
	return map[string]any{
		"_id":       k.GetID(),
		"title":     k.GetTitle(),
		"read_only": k.GetReadOnly(),
	}
}

func EncodeKey(k config.DeployKey) *github.Key {
	return &github.Key{
		Title:    github.Ptr(k.Title),
		Key:      github.Ptr(k.Key),
		ReadOnly: k.ReadOnly,
	}
}

// ---------- teams ----------

func DecodeTeam(t *github.Team) map[string]any {
	return map[string]any{
		"slug":       t.GetSlug(),
		"permission": t.GetPermission(),
	}
}

// ---------- collaborators ----------

// DecodeCollaborator prefers GitHub's role_name. The boolean permission
// map is the fallback for responses that predate it; it is ordered most
// privileged first because the flags are cumulative.
func DecodeCollaborator(u *github.User) map[string]any {
	perm := u.GetRoleName()
	if perm == "" {
		flags := u.GetPermissions()
		for _, role := range []string{"admin", "maintain", "push", "triage", "pull"} {
			if flags[role] {
				perm = role
				break
			}
		}
	}
	return map[string]any{
		"username":   u.GetLogin(),
		"permission": perm,
	}
}

// ---------- actions variables & secrets ----------

func DecodeVariable(v *github.ActionsVariable) map[string]any {
	return map[string]any{"name": v.Name, "value": v.Value}
}

// DecodeSecret carries only the name: this service manages which secrets
// exist, never their values.
func DecodeSecret(s *github.Secret) map[string]any {
	return map[string]any{"name": s.Name}
}

// ---------- environments ----------

// DecodeEnvironment projects a listed environment onto the shape
// environments.yml uses.
//
// GitHub reports an environment's settings as a `protection_rules` array
// of tagged objects, but accepts them as flat fields on the update body.
// Comparing the reported shape against the configured one reported a
// change on every field of every run, and rewrote every configured
// environment on every reconcile.
func DecodeEnvironment(e *github.Environment) map[string]any {
	out := map[string]any{"name": e.GetName()}

	for _, rule := range e.ProtectionRules {
		switch rule.GetType() {
		case "wait_timer":
			out["wait_timer"] = rule.GetWaitTimer()
		case "required_reviewers":
			if rule.PreventSelfReview != nil {
				out["prevent_self_review"] = rule.GetPreventSelfReview()
			}
			reviewers := make([]any, 0, len(rule.Reviewers))
			for _, r := range rule.Reviewers {
				reviewers = append(reviewers, map[string]any{
					"type": r.GetType(),
					"id":   reviewerID(r.Reviewer),
				})
			}
			out["reviewers"] = reviewers
		}
	}

	if p := e.DeploymentBranchPolicy; p != nil {
		policy := map[string]any{}
		if p.ProtectedBranches != nil {
			policy["protected_branches"] = p.GetProtectedBranches()
		}
		if p.CustomBranchPolicies != nil {
			policy["custom_branch_policies"] = p.GetCustomBranchPolicies()
		}
		out["deployment_branch_policy"] = policy
	}
	return out
}

// reviewerID pulls the numeric actor id out of the embedded user or team
// object and renders it the way the config spells it — as a string.
func reviewerID(reviewer any) string {
	m, ok := reviewer.(map[string]any)
	if !ok {
		return ""
	}
	switch id := m["id"].(type) {
	case float64:
		return strconv.FormatInt(int64(id), 10)
	case int64:
		return strconv.FormatInt(id, 10)
	case string:
		return id
	}
	return ""
}

func EncodeEnvironment(e config.Environment) *github.CreateUpdateEnvironment {
	out := &github.CreateUpdateEnvironment{
		WaitTimer:         e.WaitTimer,
		PreventSelfReview: e.PreventSelfReview,
	}
	// Reviewers is json:"reviewers" with no omitempty on the SDK type, so
	// a nil slice is sent as null — which GitHub reads as "no reviewers".
	// That is the correct reading of an unmanaged list here: the update
	// endpoint replaces the whole protection record.
	for _, r := range e.Reviewers {
		id, err := strconv.ParseInt(r.ID, 10, 64)
		if err != nil {
			continue // validation rejects this before we get here
		}
		out.Reviewers = append(out.Reviewers, &github.EnvReviewers{
			Type: github.Ptr(r.Type),
			ID:   github.Ptr(id),
		})
	}
	if p := e.DeploymentBranchPolicy; p != nil {
		out.DeploymentBranchPolicy = &github.BranchPolicy{
			ProtectedBranches:    p.ProtectedBranches,
			CustomBranchPolicies: p.CustomBranchPolicies,
		}
	}
	return out
}

// ---------- custom properties ----------

func EncodeCustomProperties(props map[string]any, order []string) []*github.CustomPropertyValue {
	out := make([]*github.CustomPropertyValue, 0, len(props))
	for _, name := range order {
		if v, ok := props[name]; ok {
			out = append(out, &github.CustomPropertyValue{PropertyName: name, Value: v})
		}
	}
	return out
}

func DecodeCustomProperties(values []*github.CustomPropertyValue) map[string]any {
	out := make(map[string]any, len(values))
	for _, v := range values {
		out[v.PropertyName] = v.Value
	}
	return out
}

// ---------- repository ----------

// EncodeRepoPatch turns the diffed field changes into a Repository body.
//
// It works from the change list rather than the whole config so a PATCH
// carries only what actually differs — GitHub rejects some combinations
// (visibility alongside private, archived alongside anything else) that
// a full-object write would produce on every run.
func EncodeRepoPatch(changes map[string]any) *github.Repository {
	r := &github.Repository{}
	set := map[string]func(any){
		"description":                 func(v any) { r.Description = strPtr(v) },
		"homepage":                    func(v any) { r.Homepage = strPtr(v) },
		"private":                     func(v any) { r.Private = boolPtr(v) },
		"visibility":                  func(v any) { r.Visibility = strPtr(v) },
		"has_issues":                  func(v any) { r.HasIssues = boolPtr(v) },
		"has_projects":                func(v any) { r.HasProjects = boolPtr(v) },
		"has_wiki":                    func(v any) { r.HasWiki = boolPtr(v) },
		"has_discussions":             func(v any) { r.HasDiscussions = boolPtr(v) },
		"is_template":                 func(v any) { r.IsTemplate = boolPtr(v) },
		"allow_squash_merge":          func(v any) { r.AllowSquashMerge = boolPtr(v) },
		"allow_merge_commit":          func(v any) { r.AllowMergeCommit = boolPtr(v) },
		"allow_rebase_merge":          func(v any) { r.AllowRebaseMerge = boolPtr(v) },
		"allow_auto_merge":            func(v any) { r.AllowAutoMerge = boolPtr(v) },
		"delete_branch_on_merge":      func(v any) { r.DeleteBranchOnMerge = boolPtr(v) },
		"allow_update_branch":         func(v any) { r.AllowUpdateBranch = boolPtr(v) },
		"squash_merge_commit_title":   func(v any) { r.SquashMergeCommitTitle = strPtr(v) },
		"squash_merge_commit_message": func(v any) { r.SquashMergeCommitMessage = strPtr(v) },
		"merge_commit_title":          func(v any) { r.MergeCommitTitle = strPtr(v) },
		"merge_commit_message":        func(v any) { r.MergeCommitMessage = strPtr(v) },
		"archived":                    func(v any) { r.Archived = boolPtr(v) },
		"web_commit_signoff_required": func(v any) { r.WebCommitSignoffRequired = boolPtr(v) },
	}
	for field, value := range changes {
		if apply, ok := set[field]; ok {
			apply(value)
		}
	}
	return r
}

// EncodeSecurity builds the security_and_analysis block of a repo PATCH.
//
// Only two of the five security settings live in this block. Vulnerability
// alerts, automated security fixes and private vulnerability reporting are
// each their own endpoint; folding them in here — as this service used to —
// meant GitHub ignored them, while the live read never reported them back,
// so they diffed forever and never applied.
func EncodeSecurity(changes map[string]bool) *github.Repository {
	saa := &github.SecurityAndAnalysis{}
	status := func(on bool) *string {
		if on {
			return github.Ptr("enabled")
		}
		return github.Ptr("disabled")
	}
	for field, on := range changes {
		switch field {
		case "secret_scanning":
			saa.SecretScanning = &github.SecretScanning{Status: status(on)}
		case "secret_scanning_push_protection":
			saa.SecretScanningPushProtection = &github.SecretScanningPushProtection{Status: status(on)}
		}
	}
	return &github.Repository{SecurityAndAnalysis: saa}
}

// SecurityAndAnalysisFields are the settings carried by the repo PATCH.
// The rest each need their own call.
var SecurityAndAnalysisFields = map[string]bool{
	"secret_scanning":                 true,
	"secret_scanning_push_protection": true,
}

// DecodeSecurity reads the two security_and_analysis toggles off a
// repository. The other three are read by their own endpoints.
func DecodeSecurity(r *github.Repository) map[string]any {
	out := map[string]any{}
	saa := r.GetSecurityAndAnalysis()
	if saa == nil {
		return out
	}
	if s := saa.GetSecretScanning(); s != nil {
		out["secret_scanning"] = s.GetStatus() == "enabled"
	}
	if s := saa.GetSecretScanningPushProtection(); s != nil {
		out["secret_scanning_push_protection"] = s.GetStatus() == "enabled"
	}
	return out
}

// ---------- pages ----------

func DecodePages(p *github.Pages) map[string]any {
	if p == nil {
		return nil
	}
	out := map[string]any{}
	if p.BuildType != nil {
		out["build_type"] = p.GetBuildType()
	}
	if p.CNAME != nil {
		out["cname"] = p.GetCNAME()
	}
	if p.HTTPSEnforced != nil {
		out["https_enforced"] = p.GetHTTPSEnforced()
	}
	if s := p.Source; s != nil {
		out["source"] = map[string]any{"branch": s.GetBranch(), "path": s.GetPath()}
	}
	return out
}

func EncodePagesUpdate(c config.PagesConfig) *github.PagesUpdate {
	u := &github.PagesUpdate{
		CNAME:         c.CName,
		BuildType:     c.BuildType,
		HTTPSEnforced: c.HTTPSEnforced,
	}
	if c.Source != nil {
		u.Source = &github.PagesSource{
			Branch: github.Ptr(c.Source.Branch),
			Path:   github.Ptr(c.Source.Path),
		}
	}
	return u
}

func EncodePagesCreate(c config.PagesConfig) *github.Pages {
	p := &github.Pages{BuildType: c.BuildType}
	if c.Source != nil {
		p.Source = &github.PagesSource{
			Branch: github.Ptr(c.Source.Branch),
			Path:   github.Ptr(c.Source.Path),
		}
	}
	return p
}

// ---------- actions permissions ----------

func DecodeActionsPermissions(p *github.ActionsPermissionsRepository) map[string]any {
	out := map[string]any{}
	if p == nil {
		return out
	}
	if p.Enabled != nil {
		out["enabled"] = p.GetEnabled()
	}
	if p.AllowedActions != nil {
		out["allowed_actions"] = p.GetAllowedActions()
	}
	return out
}

func EncodeActionsPermissions(c config.ActionsConfig) *github.ActionsPermissionsRepository {
	return &github.ActionsPermissionsRepository{
		Enabled:        c.Enabled,
		AllowedActions: c.AllowedActions,
	}
}

// Actions settings span three endpoints, not one.
//
// GET/PUT .../actions/permissions carries only `enabled` and
// `allowed_actions`. The allow-list itself lives on
// .../permissions/selected-actions, and the default GITHUB_TOKEN
// permissions on .../permissions/workflow. Sending all seven fields to
// the first endpoint — as this service used to — meant GitHub ignored
// five of them, and since the live read never returned them they showed
// as a pending change on every reconcile and never applied.

// ActionsAllowListFields are carried by the selected-actions endpoint.
var ActionsAllowListFields = map[string]bool{
	"github_owned_allowed": true,
	"verified_allowed":     true,
	"patterns_allowed":     true,
}

// ActionsWorkflowFields are carried by the workflow-defaults endpoint.
var ActionsWorkflowFields = map[string]bool{
	"default_workflow_permissions":     true,
	"can_approve_pull_request_reviews": true,
}

func DecodeActionsAllowed(a *github.ActionsAllowed) map[string]any {
	out := map[string]any{}
	if a == nil {
		return out
	}
	if a.GithubOwnedAllowed != nil {
		out["github_owned_allowed"] = a.GetGithubOwnedAllowed()
	}
	if a.VerifiedAllowed != nil {
		out["verified_allowed"] = a.GetVerifiedAllowed()
	}
	if a.PatternsAllowed != nil {
		out["patterns_allowed"] = toAny(a.PatternsAllowed)
	}
	return out
}

func EncodeActionsAllowed(c config.ActionsConfig) github.ActionsAllowed {
	return github.ActionsAllowed{
		GithubOwnedAllowed: c.GithubOwnedAllowed,
		VerifiedAllowed:    c.VerifiedAllowed,
		PatternsAllowed:    c.PatternsAllowed,
	}
}

func DecodeWorkflowDefaults(w *github.DefaultWorkflowPermissionRepository) map[string]any {
	out := map[string]any{}
	if w == nil {
		return out
	}
	if w.DefaultWorkflowPermissions != nil {
		out["default_workflow_permissions"] = w.GetDefaultWorkflowPermissions()
	}
	if w.CanApprovePullRequestReviews != nil {
		out["can_approve_pull_request_reviews"] = w.GetCanApprovePullRequestReviews()
	}
	return out
}

func EncodeWorkflowDefaults(c config.ActionsConfig) github.DefaultWorkflowPermissionRepository {
	return github.DefaultWorkflowPermissionRepository{
		DefaultWorkflowPermissions:   c.DefaultWorkflowPermissions,
		CanApprovePullRequestReviews: c.CanApprovePullRequestReviews,
	}
}

// ---------- shared helpers ----------

func toAny[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

func strPtr(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func boolPtr(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
