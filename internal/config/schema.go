// Package config defines the per-repo settings schema, validation, and
// loading from `.github/settings/*.yml`. Mirrors src/core/config-schema.ts
// and src/core/config-loader.ts.
//
// Pointer fields encode "field absent vs zero": a nil pointer means the
// operator did not set the field (so it's unmanaged); a non-nil pointer
// to the zero value means the field is explicitly set to that value.
// This matches Zod optional() semantics in the TS source.
package config

import "encoding/json"

// Repo identifies an org/repo pair.
type Repo struct {
	Owner string
	Name  string
}

func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Trigger names align with the TS ReconcileTrigger union.
type Trigger string

const (
	TriggerPush    Trigger = "push"
	TriggerWebhook Trigger = "webhook"
	TriggerCron    Trigger = "cron"
	TriggerManual  Trigger = "manual"
)

// PolicySeverity values for org-policy violations.
type PolicySeverity string

const (
	SevError   PolicySeverity = "error"
	SevWarning PolicySeverity = "warning"
)

// PolicyViolation flags a configured value that breaks org admin policy.
type PolicyViolation struct {
	Field     string         `json:"field"`
	Message   string         `json:"message"`
	Severity  PolicySeverity `json:"severity"`
	OrgPolicy string         `json:"org_policy"`
}

// Settings is the merged per-repo configuration. All fields are optional:
// a nil sub-struct means that resource is unmanaged.
type Settings struct {
	Repo             *RepoConfig             `json:"repo,omitempty" yaml:"repository,omitempty"`
	Topics           *[]string               `json:"topics,omitempty" yaml:"topics,omitempty"`
	Teams            *TeamsConfig            `json:"teams,omitempty" yaml:"-"`
	Rulesets         *RulesetsConfig         `json:"rulesets,omitempty" yaml:"-"`
	Branches         *BranchesConfig         `json:"branches,omitempty" yaml:"-"`
	Environments     *EnvironmentsConfig     `json:"environments,omitempty" yaml:"-"`
	Webhooks         *WebhooksConfig         `json:"webhooks,omitempty" yaml:"-"`
	Autolinks        *AutolinksConfig        `json:"autolinks,omitempty" yaml:"-"`
	Actions          *ActionsConfig          `json:"actions,omitempty" yaml:"-"`
	Security         *SecurityConfig         `json:"security,omitempty" yaml:"-"`
	Pages            *PagesConfig            `json:"pages,omitempty" yaml:"-"`
	Secrets          *SecretsConfig          `json:"secrets,omitempty" yaml:"-"`
	Variables        *VariablesConfig        `json:"variables,omitempty" yaml:"-"`
	DeployKeys       *DeployKeysConfig       `json:"deploy_keys,omitempty" yaml:"-"`
	CustomProperties *CustomPropertiesConfig `json:"custom_properties,omitempty" yaml:"-"`
	Collaborators    *CollaboratorsConfig    `json:"collaborators,omitempty" yaml:"-"`
}

// RepoConfig matches GitHub's PATCH /repos/:owner/:repo body.
type RepoConfig struct {
	Description              *string `yaml:"description,omitempty" json:"description,omitempty"`
	Homepage                 *string `yaml:"homepage,omitempty" json:"homepage,omitempty"`
	Private                  *bool   `yaml:"private,omitempty" json:"private,omitempty"`
	Visibility               *string `yaml:"visibility,omitempty" json:"visibility,omitempty"`
	HasIssues                *bool   `yaml:"has_issues,omitempty" json:"has_issues,omitempty"`
	HasProjects              *bool   `yaml:"has_projects,omitempty" json:"has_projects,omitempty"`
	HasWiki                  *bool   `yaml:"has_wiki,omitempty" json:"has_wiki,omitempty"`
	HasDiscussions           *bool   `yaml:"has_discussions,omitempty" json:"has_discussions,omitempty"`
	IsTemplate               *bool   `yaml:"is_template,omitempty" json:"is_template,omitempty"`
	AllowSquashMerge         *bool   `yaml:"allow_squash_merge,omitempty" json:"allow_squash_merge,omitempty"`
	AllowMergeCommit         *bool   `yaml:"allow_merge_commit,omitempty" json:"allow_merge_commit,omitempty"`
	AllowRebaseMerge         *bool   `yaml:"allow_rebase_merge,omitempty" json:"allow_rebase_merge,omitempty"`
	AllowAutoMerge           *bool   `yaml:"allow_auto_merge,omitempty" json:"allow_auto_merge,omitempty"`
	DeleteBranchOnMerge      *bool   `yaml:"delete_branch_on_merge,omitempty" json:"delete_branch_on_merge,omitempty"`
	AllowUpdateBranch        *bool   `yaml:"allow_update_branch,omitempty" json:"allow_update_branch,omitempty"`
	SquashMergeCommitTitle   *string `yaml:"squash_merge_commit_title,omitempty" json:"squash_merge_commit_title,omitempty"`
	SquashMergeCommitMessage *string `yaml:"squash_merge_commit_message,omitempty" json:"squash_merge_commit_message,omitempty"`
	MergeCommitTitle         *string `yaml:"merge_commit_title,omitempty" json:"merge_commit_title,omitempty"`
	MergeCommitMessage       *string `yaml:"merge_commit_message,omitempty" json:"merge_commit_message,omitempty"`
	Archived                 *bool   `yaml:"archived,omitempty" json:"archived,omitempty"`
	WebCommitSignoffRequired *bool   `yaml:"web_commit_signoff_required,omitempty" json:"web_commit_signoff_required,omitempty"`
}

// --- Teams ---

type TeamPermission string

const (
	PermPull     TeamPermission = "pull"
	PermTriage   TeamPermission = "triage"
	PermPush     TeamPermission = "push"
	PermMaintain TeamPermission = "maintain"
	PermAdmin    TeamPermission = "admin"
)

type TeamAccess struct {
	Slug       string         `yaml:"slug" json:"slug"`
	Permission TeamPermission `yaml:"permission" json:"permission"`
}

type TeamsConfig struct {
	Teams []TeamAccess `yaml:"teams" json:"teams"`
}

// --- Rulesets ---

type RulesetBypassActor struct {
	ActorType  string `yaml:"actor_type" json:"actor_type"`
	ActorID    any    `yaml:"actor_id,omitempty" json:"actor_id,omitempty"`
	BypassMode string `yaml:"bypass_mode" json:"bypass_mode"`
}

type RulesetCondition struct {
	RefName struct {
		Include []string `yaml:"include" json:"include"`
		Exclude []string `yaml:"exclude" json:"exclude"`
	} `yaml:"ref_name" json:"ref_name"`
}

type RulesetRule struct {
	Type       string         `yaml:"type" json:"type"`
	Parameters map[string]any `yaml:"parameters,omitempty" json:"parameters,omitempty"`
}

type Ruleset struct {
	Name         string               `yaml:"name" json:"name"`
	Target       string               `yaml:"target" json:"target"`
	Enforcement  string               `yaml:"enforcement" json:"enforcement"`
	Conditions   *RulesetCondition    `yaml:"conditions,omitempty" json:"conditions,omitempty"`
	BypassActors []RulesetBypassActor `yaml:"bypass_actors,omitempty" json:"bypass_actors,omitempty"`
	Rules        []RulesetRule        `yaml:"rules" json:"rules"`
}

type RulesetsConfig struct {
	Rulesets []Ruleset `yaml:"rulesets" json:"rulesets"`
}

// --- Branches ---

type RequiredStatusChecks struct {
	Strict   *bool    `yaml:"strict,omitempty" json:"strict,omitempty"`
	Contexts []string `yaml:"contexts,omitempty" json:"contexts,omitempty"`
}

type RequiredPRReviews struct {
	RequiredApprovingReviewCount *int  `yaml:"required_approving_review_count,omitempty" json:"required_approving_review_count,omitempty"`
	DismissStaleReviews          *bool `yaml:"dismiss_stale_reviews,omitempty" json:"dismiss_stale_reviews,omitempty"`
	RequireCodeOwnerReviews      *bool `yaml:"require_code_owner_reviews,omitempty" json:"require_code_owner_reviews,omitempty"`
	RequireLastPushApproval      *bool `yaml:"require_last_push_approval,omitempty" json:"require_last_push_approval,omitempty"`
}

type BranchRestrictions struct {
	Users []string  `yaml:"users,omitempty" json:"users,omitempty"`
	Teams []string  `yaml:"teams,omitempty" json:"teams,omitempty"`
	Apps  *[]string `yaml:"apps,omitempty" json:"apps,omitempty"`
}

// BranchProtection is the one section where configuring a branch takes
// ownership of that branch's ENTIRE protection record.
//
// GitHub's PUT .../branches/{branch}/protection is a whole-object
// replace, not a merge: the four sub-objects below must all be present
// in the request (each accepting null to mean "off"), and the optional
// booleans default to false when omitted. There is no way to ask the
// endpoint to leave one setting alone. So, uniquely here, the json tags
// deliberately drop `omitempty`: the desired document states the full
// record, and the dry-run therefore shows exactly what the apply will
// write. Normalize() fills the rest.
//
// The yaml tags keep `omitempty` — every field is still optional to
// write; it is the wire contract, not the user contract, that demands
// completeness.
type BranchProtection struct {
	Pattern                        string                `yaml:"pattern" json:"pattern"`
	RequiredStatusChecks           *RequiredStatusChecks `yaml:"required_status_checks,omitempty" json:"required_status_checks"`
	EnforceAdmins                  *bool                 `yaml:"enforce_admins,omitempty" json:"enforce_admins"`
	RequiredPullRequestReviews     *RequiredPRReviews    `yaml:"required_pull_request_reviews,omitempty" json:"required_pull_request_reviews"`
	Restrictions                   *BranchRestrictions   `yaml:"restrictions,omitempty" json:"restrictions"`
	RequiredLinearHistory          *bool                 `yaml:"required_linear_history,omitempty" json:"required_linear_history,omitempty"`
	AllowForcePushes               *bool                 `yaml:"allow_force_pushes,omitempty" json:"allow_force_pushes,omitempty"`
	AllowDeletions                 *bool                 `yaml:"allow_deletions,omitempty" json:"allow_deletions,omitempty"`
	RequiredConversationResolution *bool                 `yaml:"required_conversation_resolution,omitempty" json:"required_conversation_resolution,omitempty"`

	// RequiredSignatures is a separate GitHub sub-resource
	// (.../protection/required_signatures), not a field of the
	// protection body. It stays in the desired document so the diff
	// reports it; the encoder strips it from the PUT and the lane
	// applies it with its own call.
	RequiredSignatures *bool `yaml:"required_signatures,omitempty" json:"required_signatures,omitempty"`
}

type BranchesConfig struct {
	Branches []BranchProtection `yaml:"branches" json:"branches"`
}

// --- Environments ---

type EnvReviewer struct {
	Type string `yaml:"type" json:"type"`
	ID   string `yaml:"id" json:"id"`
}

type EnvVariable struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value" json:"value"`
}

type EnvSecretRef struct {
	Name string `yaml:"name" json:"name"`
}

type DeploymentBranchPolicy struct {
	ProtectedBranches    *bool         `yaml:"protected_branches,omitempty" json:"protected_branches,omitempty"`
	CustomBranchPolicies *bool         `yaml:"custom_branch_policies,omitempty" json:"custom_branch_policies,omitempty"`
	BranchPolicies       []BranchEntry `yaml:"branch_policies,omitempty" json:"branch_policies,omitempty"`
}

type BranchEntry struct {
	Name string `yaml:"name" json:"name"`
}

type Environment struct {
	Name string `yaml:"name" json:"name"`
	//nolint:unused // Name is the resource key; Encode strips it from the body.
	WaitTimer              *int                    `yaml:"wait_timer,omitempty" json:"wait_timer,omitempty"`
	PreventSelfReview      *bool                   `yaml:"prevent_self_review,omitempty" json:"prevent_self_review,omitempty"`
	Reviewers              []EnvReviewer           `yaml:"reviewers,omitempty" json:"reviewers,omitempty"`
	DeploymentBranchPolicy *DeploymentBranchPolicy `yaml:"deployment_branch_policy,omitempty" json:"deployment_branch_policy,omitempty"`

	// Variables / Secrets are part of the YAML contract but are not
	// applied yet, and they are not fields on GitHub's environment
	// endpoint — `json:"-"` keeps them out of both the diff document
	// and the request body. Validate reports them as unsupported.
	Variables []EnvVariable  `yaml:"variables,omitempty" json:"-"`
	Secrets   []EnvSecretRef `yaml:"secrets,omitempty" json:"-"`
}

type EnvironmentsConfig struct {
	Environments []Environment `yaml:"environments" json:"environments"`
}

// --- Webhooks ---

type Webhook struct {
	URL         string   `yaml:"url" json:"url"`
	ContentType *string  `yaml:"content_type,omitempty" json:"content_type,omitempty"`
	InsecureSSL *bool    `yaml:"insecure_ssl,omitempty" json:"insecure_ssl,omitempty"`
	Active      *bool    `yaml:"active,omitempty" json:"active,omitempty"`
	Events      []string `yaml:"events,omitempty" json:"events,omitempty"`

	// SecretRef names a webhook secret held outside this repo. It is
	// part of the user-facing YAML contract but is NOT a GitHub wire
	// field and must never reach the diff document, hence `json:"-"`.
	// Not applied yet — Validate reports it as unsupported.
	SecretRef string `yaml:"secret_ref,omitempty" json:"-"`
}

type WebhooksConfig struct {
	Webhooks []Webhook `yaml:"webhooks" json:"webhooks"`
}

// --- Autolinks ---

type Autolink struct {
	KeyPrefix      string `yaml:"key_prefix" json:"key_prefix"`
	URLTemplate    string `yaml:"url_template" json:"url_template"`
	IsAlphanumeric *bool  `yaml:"is_alphanumeric,omitempty" json:"is_alphanumeric,omitempty"`
}

type AutolinksConfig struct {
	Autolinks []Autolink `yaml:"autolinks" json:"autolinks"`
}

// --- Actions ---

type ActionsConfig struct {
	Enabled                      *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	AllowedActions               *string  `yaml:"allowed_actions,omitempty" json:"allowed_actions,omitempty"`
	GithubOwnedAllowed           *bool    `yaml:"github_owned_allowed,omitempty" json:"github_owned_allowed,omitempty"`
	VerifiedAllowed              *bool    `yaml:"verified_allowed,omitempty" json:"verified_allowed,omitempty"`
	PatternsAllowed              []string `yaml:"patterns_allowed,omitempty" json:"patterns_allowed,omitempty"`
	DefaultWorkflowPermissions   *string  `yaml:"default_workflow_permissions,omitempty" json:"default_workflow_permissions,omitempty"`
	CanApprovePullRequestReviews *bool    `yaml:"can_approve_pull_request_reviews,omitempty" json:"can_approve_pull_request_reviews,omitempty"`
}

// --- Security ---

type SecurityConfig struct {
	VulnerabilityAlerts           *bool `yaml:"vulnerability_alerts,omitempty" json:"vulnerability_alerts,omitempty"`
	AutomatedSecurityFixes        *bool `yaml:"automated_security_fixes,omitempty" json:"automated_security_fixes,omitempty"`
	SecretScanning                *bool `yaml:"secret_scanning,omitempty" json:"secret_scanning,omitempty"`
	SecretScanningPushProtection  *bool `yaml:"secret_scanning_push_protection,omitempty" json:"secret_scanning_push_protection,omitempty"`
	PrivateVulnerabilityReporting *bool `yaml:"private_vulnerability_reporting,omitempty" json:"private_vulnerability_reporting,omitempty"`
}

// --- Pages ---

type PagesSource struct {
	Branch string `yaml:"branch" json:"branch"`
	Path   string `yaml:"path" json:"path"`
}

type PagesConfig struct {
	BuildType     *string      `yaml:"build_type,omitempty" json:"build_type,omitempty"`
	Source        *PagesSource `yaml:"source,omitempty" json:"source,omitempty"`
	CName         *string      `yaml:"cname,omitempty" json:"cname,omitempty"`
	HTTPSEnforced *bool        `yaml:"https_enforced,omitempty" json:"https_enforced,omitempty"`
}

// --- Secrets / Variables / DeployKeys / CustomProperties / Collaborators ---

type SecretRef struct {
	Name string `yaml:"name" json:"name"`
}

type SecretsConfig struct {
	RepositorySecrets []SecretRef `yaml:"repository_secrets" json:"repository_secrets"`
}

type Variable struct {
	Name  string `yaml:"name" json:"name"`
	Value string `yaml:"value" json:"value"`
}

type VariablesConfig struct {
	Variables []Variable `yaml:"variables" json:"variables"`
}

type DeployKey struct {
	Title    string `yaml:"title" json:"title"`
	ReadOnly *bool  `yaml:"read_only,omitempty" json:"read_only,omitempty"`

	// Key / KeyRef carry the material itself. They are user-facing
	// YAML only: the public key is never diffed (GitHub never returns
	// it) and must not land in the diff document.
	KeyRef string `yaml:"key_ref,omitempty" json:"-"`
	Key    string `yaml:"key,omitempty" json:"-"`
}

type DeployKeysConfig struct {
	DeployKeys []DeployKey `yaml:"deploy_keys" json:"deploy_keys"`
}

// CustomPropertiesConfig is a free-form map; values are string,
// []string, or bool. yaml.v3 unmarshals these into the corresponding
// Go types automatically.
type CustomPropertiesConfig struct {
	Properties map[string]any `yaml:"custom_properties" json:"custom_properties"`
}

func (c CustomPropertiesConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.Properties)
}

type Collaborator struct {
	Username   string         `yaml:"username" json:"username"`
	Permission TeamPermission `yaml:"permission" json:"permission"`
}

type CollaboratorsConfig struct {
	Collaborators []Collaborator `yaml:"collaborators" json:"collaborators"`
}

// FileVersion is the discriminator at the top of every settings file.
type FileVersion struct {
	Version int `yaml:"_version" json:"_version"`
}

// Ptr returns a pointer to v. Optional scalars in this schema are
// pointers so that "the operator did not mention this field" stays
// distinguishable from "the operator set it to the zero value" — see
// the package doc. Ptr is the ergonomic way to build a literal:
//
//	Webhook{URL: u, Active: config.Ptr(false)}
func Ptr[T any](v T) *T { return &v }
