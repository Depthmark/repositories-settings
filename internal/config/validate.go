package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ValidationError carries one or more field-level validation issues. It
// is returned in the /api/validate response body and surfaces in PR
// checks.
type ValidationError struct {
	File   string   `json:"file,omitempty"`
	Issues []string `json:"issues"`
}

func (e *ValidationError) Error() string {
	if e.File != "" {
		return fmt.Sprintf("%s: %s", e.File, strings.Join(e.Issues, "; "))
	}
	return strings.Join(e.Issues, "; ")
}

func (e *ValidationError) add(format string, args ...any) {
	e.Issues = append(e.Issues, fmt.Sprintf(format, args...))
}

func (e *ValidationError) ok() bool { return len(e.Issues) == 0 }

// ValidateRepoFile validates the structure decoded from repo.yml.
func ValidateRepoFile(f *RepoFile) error {
	v := &ValidationError{File: "repo.yml"}
	requireVersion(v, f.Version)
	if f.Repository != nil {
		validateRepo(v, f.Repository)
	}
	if f.Topics != nil {
		validateTopics(v, *f.Topics)
	}
	return finalize(v)
}

func ValidateTeamsFile(f *TeamsFile) error {
	v := &ValidationError{File: "teams.yml"}
	requireVersion(v, f.Version)
	if len(f.Teams) == 0 {
		v.add("teams: at least one team required")
	}
	for i, t := range f.Teams {
		if t.Slug == "" {
			v.add("teams[%d].slug: required", i)
		}
		if !isValidPermission(string(t.Permission)) {
			v.add("teams[%d].permission: invalid value %q", i, t.Permission)
		}
	}
	return finalize(v)
}

func ValidateRulesetsFile(f *RulesetsFile) error {
	v := &ValidationError{File: "rulesets.yml"}
	requireVersion(v, f.Version)
	if len(f.Rulesets) == 0 {
		v.add("rulesets: at least one ruleset required")
	}
	for i, r := range f.Rulesets {
		if r.Name == "" || len(r.Name) > 100 {
			v.add("rulesets[%d].name: required, max 100 chars", i)
		}
		if !oneOf(r.Target, "branch", "tag") {
			v.add("rulesets[%d].target: must be branch or tag", i)
		}
		if !oneOf(r.Enforcement, "active", "disabled", "evaluate") {
			v.add("rulesets[%d].enforcement: invalid value %q", i, r.Enforcement)
		}
		if len(r.Rules) == 0 {
			v.add("rulesets[%d].rules: at least one rule required", i)
		}
	}
	return finalize(v)
}

func ValidateEnvironmentsFile(f *EnvironmentsFile) error {
	v := &ValidationError{File: "environments.yml"}
	requireVersion(v, f.Version)
	if len(f.Environments) == 0 {
		v.add("environments: at least one environment required")
	}
	for i, e := range f.Environments {
		if e.Name == "" {
			v.add("environments[%d].name: required", i)
		}
		if e.WaitTimer != nil && (*e.WaitTimer < 0 || *e.WaitTimer > 43200) {
			v.add("environments[%d].wait_timer: must be 0..43200", i)
		}
		if len(e.Reviewers) > 6 {
			v.add("environments[%d].reviewers: max 6", i)
		}
	}
	return finalize(v)
}

var urlSchemeRE = regexp.MustCompile(`^https?://`)

func ValidateWebhooksFile(f *WebhooksFile) error {
	v := &ValidationError{File: "webhooks.yml"}
	requireVersion(v, f.Version)
	if len(f.Webhooks) == 0 {
		v.add("webhooks: at least one webhook required")
	}
	for i, w := range f.Webhooks {
		if w.URL == "" {
			v.add("webhooks[%d].url: required", i)
		} else if u, err := url.Parse(w.URL); err != nil || !urlSchemeRE.MatchString(w.URL) {
			_ = u
			v.add("webhooks[%d].url: must be a valid http(s) URL", i)
		}
		if w.ContentType != "" && !oneOf(w.ContentType, "json", "form") {
			v.add("webhooks[%d].content_type: invalid value %q", i, w.ContentType)
		}
		if len(w.Events) == 0 {
			v.add("webhooks[%d].events: at least one event required", i)
		}
	}
	return finalize(v)
}

func ValidateAutolinksFile(f *AutolinksFile) error {
	v := &ValidationError{File: "autolinks.yml"}
	requireVersion(v, f.Version)
	if len(f.Autolinks) == 0 {
		v.add("autolinks: at least one autolink required")
	}
	for i, a := range f.Autolinks {
		if a.KeyPrefix == "" {
			v.add("autolinks[%d].key_prefix: required", i)
		}
		if a.URLTemplate == "" {
			v.add("autolinks[%d].url_template: required", i)
		}
	}
	return finalize(v)
}

func ValidateActionsFile(f *ActionsFile) error {
	v := &ValidationError{File: "actions.yml"}
	requireVersion(v, f.Version)
	if f.Actions.AllowedActions != nil &&
		!oneOf(*f.Actions.AllowedActions, "all", "local_only", "selected") {
		v.add("actions.allowed_actions: invalid value %q", *f.Actions.AllowedActions)
	}
	if f.Actions.DefaultWorkflowPermissions != nil &&
		!oneOf(*f.Actions.DefaultWorkflowPermissions, "read", "write") {
		v.add("actions.default_workflow_permissions: invalid value %q", *f.Actions.DefaultWorkflowPermissions)
	}
	return finalize(v)
}

func ValidateSecurityFile(f *SecurityFile) error {
	v := &ValidationError{File: "security.yml"}
	requireVersion(v, f.Version)
	return finalize(v)
}

func ValidatePagesFile(f *PagesFile) error {
	v := &ValidationError{File: "pages.yml"}
	requireVersion(v, f.Version)
	if f.Pages.BuildType != nil && !oneOf(*f.Pages.BuildType, "legacy", "workflow") {
		v.add("pages.build_type: invalid value %q", *f.Pages.BuildType)
	}
	if f.Pages.Source != nil {
		if f.Pages.Source.Branch == "" {
			v.add("pages.source.branch: required")
		}
		if !oneOf(f.Pages.Source.Path, "/", "/docs") {
			v.add("pages.source.path: must be / or /docs")
		}
	}
	return finalize(v)
}

var secretNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

func ValidateSecretsFile(f *SecretsFile) error {
	v := &ValidationError{File: "secrets.yml"}
	requireVersion(v, f.Version)
	for i, s := range f.RepositorySecrets {
		if !secretNameRE.MatchString(s.Name) {
			v.add("repository_secrets[%d].name: must match [A-Z_][A-Z0-9_]*", i)
		}
	}
	return finalize(v)
}

func ValidateVariablesFile(f *VariablesFile) error {
	v := &ValidationError{File: "variables.yml"}
	requireVersion(v, f.Version)
	if len(f.Variables) == 0 {
		v.add("variables: at least one variable required")
	}
	for i, va := range f.Variables {
		if !secretNameRE.MatchString(va.Name) {
			v.add("variables[%d].name: must match [A-Z_][A-Z0-9_]*", i)
		}
	}
	return finalize(v)
}

func ValidateDeployKeysFile(f *DeployKeysFile) error {
	v := &ValidationError{File: "deploy-keys.yml"}
	requireVersion(v, f.Version)
	if len(f.DeployKeys) == 0 {
		v.add("deploy_keys: at least one key required")
	}
	for i, k := range f.DeployKeys {
		if k.Title == "" {
			v.add("deploy_keys[%d].title: required", i)
		}
		if k.Key == "" && k.KeyRef == "" {
			v.add("deploy_keys[%d]: either key or key_ref must be set", i)
		}
	}
	return finalize(v)
}

func ValidateCustomPropertiesFile(f *CustomPropertiesFile) error {
	v := &ValidationError{File: "custom-properties.yml"}
	requireVersion(v, f.Version)
	if len(f.CustomProperties) == 0 {
		v.add("custom_properties: at least one property required")
	}
	for k, val := range f.CustomProperties {
		switch val.(type) {
		case string, bool:
			// ok
		case []any:
			for j, item := range val.([]any) {
				if _, ok := item.(string); !ok {
					v.add("custom_properties.%s[%d]: array values must be strings", k, j)
				}
			}
		default:
			v.add("custom_properties.%s: must be string, []string or bool", k)
		}
	}
	return finalize(v)
}

func ValidateCollaboratorsFile(f *CollaboratorsFile) error {
	v := &ValidationError{File: "collaborators.yml"}
	requireVersion(v, f.Version)
	if len(f.Collaborators) == 0 {
		v.add("collaborators: at least one collaborator required")
	}
	for i, c := range f.Collaborators {
		if c.Username == "" {
			v.add("collaborators[%d].username: required", i)
		}
		if !isValidPermission(string(c.Permission)) {
			v.add("collaborators[%d].permission: invalid value %q", i, c.Permission)
		}
	}
	return finalize(v)
}

func ValidateBranchesFile(f *BranchesFile) error {
	v := &ValidationError{File: "branches.yml"}
	requireVersion(v, f.Version)
	if len(f.Branches) == 0 {
		v.add("branches: at least one entry required")
	}
	for i, b := range f.Branches {
		if b.Pattern == "" {
			v.add("branches[%d].pattern: required", i)
		}
		if b.RequiredPullRequestReviews != nil &&
			b.RequiredPullRequestReviews.RequiredApprovingReviewCount != nil {
			n := *b.RequiredPullRequestReviews.RequiredApprovingReviewCount
			if n < 0 || n > 6 {
				v.add("branches[%d].required_pull_request_reviews.required_approving_review_count: must be 0..6", i)
			}
		}
	}
	return finalize(v)
}

// helpers

func requireVersion(v *ValidationError, ver int) {
	if ver != 1 {
		v.add("_version: expected 1, got %d", ver)
	}
}

func isValidPermission(p string) bool {
	return oneOf(p, "pull", "triage", "push", "maintain", "admin")
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

var topicRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func validateTopics(v *ValidationError, topics []string) {
	if len(topics) > 20 {
		v.add("topics: max 20")
	}
	for i, t := range topics {
		if len(t) > 50 || !topicRE.MatchString(t) {
			v.add("topics[%d]: must match %s, max 50 chars", i, topicRE.String())
		}
	}
}

func validateRepo(v *ValidationError, r *RepoConfig) {
	if r.Visibility != nil && !oneOf(*r.Visibility, "public", "private", "internal") {
		v.add("repository.visibility: invalid value %q", *r.Visibility)
	}
	if r.Homepage != nil && *r.Homepage != "" {
		if u, err := url.Parse(*r.Homepage); err != nil || u.Scheme == "" {
			v.add("repository.homepage: must be a valid URL")
		}
	}
	if r.SquashMergeCommitTitle != nil &&
		!oneOf(*r.SquashMergeCommitTitle, "PR_TITLE", "COMMIT_OR_PR_TITLE") {
		v.add("repository.squash_merge_commit_title: invalid value")
	}
	if r.SquashMergeCommitMessage != nil &&
		!oneOf(*r.SquashMergeCommitMessage, "PR_BODY", "COMMIT_MESSAGES", "BLANK") {
		v.add("repository.squash_merge_commit_message: invalid value")
	}
	if r.MergeCommitTitle != nil &&
		!oneOf(*r.MergeCommitTitle, "PR_TITLE", "MERGE_MESSAGE") {
		v.add("repository.merge_commit_title: invalid value")
	}
	if r.MergeCommitMessage != nil &&
		!oneOf(*r.MergeCommitMessage, "PR_BODY", "PR_TITLE", "BLANK") {
		v.add("repository.merge_commit_message: invalid value")
	}
}

func finalize(v *ValidationError) error {
	if v.ok() {
		return nil
	}
	return v
}
