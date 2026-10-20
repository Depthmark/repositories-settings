package config

// AdminPolicy is the org-level policy file (`policy.yml` at the org admin
// repo). It encodes which repo-level overrides are allowed, locked
// fields, team permission constraints, etc.
type AdminPolicy struct {
	Repo         *PolicyRepo         `yaml:"repo,omitempty" json:"repo,omitempty"`
	Rulesets     *PolicyRulesets     `yaml:"rulesets,omitempty" json:"rulesets,omitempty"`
	Teams        *PolicyTeams        `yaml:"teams,omitempty" json:"teams,omitempty"`
	Security     *PolicySecurity     `yaml:"security,omitempty" json:"security,omitempty"`
	Actions      *PolicyActions      `yaml:"actions,omitempty" json:"actions,omitempty"`
	Environments *PolicyEnvironments `yaml:"environments,omitempty" json:"environments,omitempty"`
}

type PolicyFile struct {
	Version int         `yaml:"_version"`
	Policy  AdminPolicy `yaml:"policy"`
}

type PolicyRepo struct {
	AllowedOverrides []string `yaml:"allowed_overrides,omitempty" json:"allowed_overrides,omitempty"`
	Locked           []string `yaml:"locked,omitempty" json:"locked,omitempty"`
}

type PolicyRulesets struct {
	AllowRepoLevel       *bool `yaml:"allow_repo_level,omitempty" json:"allow_repo_level,omitempty"`
	MinRequiredApprovers *int  `yaml:"min_required_approvers,omitempty" json:"min_required_approvers,omitempty"`
}

type PolicyTeamConstraint struct {
	Slug          string         `yaml:"slug" json:"slug"`
	MinPermission TeamPermission `yaml:"min_permission" json:"min_permission"`
}

type PolicyTeamPermissionByPattern struct {
	Pattern            string           `yaml:"pattern" json:"pattern"`
	AllowedPermissions []TeamPermission `yaml:"allowed_permissions" json:"allowed_permissions"`
}

type PolicyTeams struct {
	AllowRepoLevel      *bool                           `yaml:"allow_repo_level,omitempty" json:"allow_repo_level,omitempty"`
	MaxPermission       *TeamPermission                 `yaml:"max_permission,omitempty" json:"max_permission,omitempty"`
	RequiredTeams       []PolicyTeamConstraint          `yaml:"required_teams,omitempty" json:"required_teams,omitempty"`
	PermissionByPattern []PolicyTeamPermissionByPattern `yaml:"permission_by_pattern,omitempty" json:"permission_by_pattern,omitempty"`
}

type PolicySecurity struct {
	Locked *bool `yaml:"locked,omitempty" json:"locked,omitempty"`
}

type PolicyActions struct {
	AllowRepoLevel *bool `yaml:"allow_repo_level,omitempty" json:"allow_repo_level,omitempty"`
}

type PolicyEnvironments struct {
	AllowRepoLevel              *bool `yaml:"allow_repo_level,omitempty" json:"allow_repo_level,omitempty"`
	ProductionRequiresReviewers *bool `yaml:"production_requires_reviewers,omitempty" json:"production_requires_reviewers,omitempty"`
	ProductionMinReviewers      *int  `yaml:"production_min_reviewers,omitempty" json:"production_min_reviewers,omitempty"`
}
