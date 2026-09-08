package config

import "strings"

// fileToKeys maps `.github/settings/*.yml` paths to the applier keys they
// affect. repo.yml affects both "repo" and "topics".
var fileToKeys = map[string][]string{
	"repo.yml":              {"repo", "topics"},
	"teams.yml":             {"teams"},
	"rulesets.yml":          {"rulesets"},
	"branches.yml":          {"branches"},
	"environments.yml":      {"environments"},
	"webhooks.yml":          {"webhooks"},
	"autolinks.yml":         {"autolinks"},
	"actions.yml":           {"actions"},
	"security.yml":          {"security"},
	"pages.yml":             {"pages"},
	"secrets.yml":           {"secrets"},
	"variables.yml":         {"variables"},
	"deploy-keys.yml":       {"deployKeys"},
	"custom-properties.yml": {"customProperties"},
	"collaborators.yml":     {"collaborators"},
}

// AffectedKeys returns the applier keys touched by the given changed
// files. Returns nil to mean "run everything" when no list was provided
// or when an unknown file under the settings dir was changed.
func AffectedKeys(changed []string) map[string]struct{} {
	if len(changed) == 0 {
		return nil
	}
	keys := make(map[string]struct{})
	for _, f := range changed {
		rel := f
		if strings.HasPrefix(f, SettingsDirPrefix) {
			rel = strings.TrimPrefix(f, SettingsDirPrefix)
		}
		mapped, ok := fileToKeys[rel]
		if !ok {
			// Unknown file in settings dir — run everything.
			return nil
		}
		for _, k := range mapped {
			keys[k] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

// Filter returns a new Settings retaining only sections whose key is in
// `keys`. If keys is nil, the original is returned as-is.
func Filter(s *Settings, keys map[string]struct{}) *Settings {
	if keys == nil || s == nil {
		return s
	}
	out := &Settings{}
	if _, ok := keys["repo"]; ok {
		out.Repo = s.Repo
	}
	if _, ok := keys["topics"]; ok {
		out.Topics = s.Topics
	}
	if _, ok := keys["teams"]; ok {
		out.Teams = s.Teams
	}
	if _, ok := keys["rulesets"]; ok {
		out.Rulesets = s.Rulesets
	}
	if _, ok := keys["branches"]; ok {
		out.Branches = s.Branches
	}
	if _, ok := keys["environments"]; ok {
		out.Environments = s.Environments
	}
	if _, ok := keys["webhooks"]; ok {
		out.Webhooks = s.Webhooks
	}
	if _, ok := keys["autolinks"]; ok {
		out.Autolinks = s.Autolinks
	}
	if _, ok := keys["actions"]; ok {
		out.Actions = s.Actions
	}
	if _, ok := keys["security"]; ok {
		out.Security = s.Security
	}
	if _, ok := keys["pages"]; ok {
		out.Pages = s.Pages
	}
	if _, ok := keys["secrets"]; ok {
		out.Secrets = s.Secrets
	}
	if _, ok := keys["variables"]; ok {
		out.Variables = s.Variables
	}
	if _, ok := keys["deployKeys"]; ok {
		out.DeployKeys = s.DeployKeys
	}
	if _, ok := keys["customProperties"]; ok {
		out.CustomProperties = s.CustomProperties
	}
	if _, ok := keys["collaborators"]; ok {
		out.Collaborators = s.Collaborators
	}
	return out
}
