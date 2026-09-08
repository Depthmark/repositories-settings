package config

// File envelopes — what each YAML file under .github/settings/ looks like.
// Loader reads into one of these per filename, then folds the inner config
// into Settings.

type RepoFile struct {
	Version    int         `yaml:"_version"`
	Repository *RepoConfig `yaml:"repository,omitempty"`
	Topics     *[]string   `yaml:"topics,omitempty"`
}

type TeamsFile struct {
	Version int          `yaml:"_version"`
	Teams   []TeamAccess `yaml:"teams"`
}

type RulesetsFile struct {
	Version  int       `yaml:"_version"`
	Rulesets []Ruleset `yaml:"rulesets"`
}

type EnvironmentsFile struct {
	Version      int           `yaml:"_version"`
	Environments []Environment `yaml:"environments"`
}

type WebhooksFile struct {
	Version  int       `yaml:"_version"`
	Webhooks []Webhook `yaml:"webhooks"`
}

type AutolinksFile struct {
	Version   int        `yaml:"_version"`
	Autolinks []Autolink `yaml:"autolinks"`
}

type ActionsFile struct {
	Version int           `yaml:"_version"`
	Actions ActionsConfig `yaml:"actions"`
}

type SecurityFile struct {
	Version  int            `yaml:"_version"`
	Security SecurityConfig `yaml:"security"`
}

type PagesFile struct {
	Version int         `yaml:"_version"`
	Pages   PagesConfig `yaml:"pages"`
}

type SecretsFile struct {
	Version           int         `yaml:"_version"`
	RepositorySecrets []SecretRef `yaml:"repository_secrets"`
}

type VariablesFile struct {
	Version   int        `yaml:"_version"`
	Variables []Variable `yaml:"variables"`
}

type DeployKeysFile struct {
	Version    int         `yaml:"_version"`
	DeployKeys []DeployKey `yaml:"deploy_keys"`
}

type CustomPropertiesFile struct {
	Version          int            `yaml:"_version"`
	CustomProperties map[string]any `yaml:"custom_properties"`
}

type CollaboratorsFile struct {
	Version       int            `yaml:"_version"`
	Collaborators []Collaborator `yaml:"collaborators"`
}

type BranchesFile struct {
	Version  int                `yaml:"_version"`
	Branches []BranchProtection `yaml:"branches"`
}

// FileNames is the canonical list of settings files; ConfigKeys returns the
// matching applier keys. SettingsDirPrefix is where they live in a repo.
const SettingsDirPrefix = ".github/settings/"

var FileNames = []string{
	"repo.yml",
	"teams.yml",
	"rulesets.yml",
	"branches.yml",
	"environments.yml",
	"webhooks.yml",
	"autolinks.yml",
	"actions.yml",
	"security.yml",
	"pages.yml",
	"secrets.yml",
	"variables.yml",
	"deploy-keys.yml",
	"custom-properties.yml",
	"collaborators.yml",
}
