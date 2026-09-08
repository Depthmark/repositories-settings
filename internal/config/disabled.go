package config

import "strings"

// DisabledResources is the operator-level deny-set: lanes whose key is
// in this set are skipped during reconcile, regardless of what the
// repo's YAML asks for. This is the compliance/lockdown layer
// ("nobody on this deployment can write secrets via repo-settings"),
// distinct from per-org admin policy.
//
// Keys are the canonical Lane.Resource strings (snake_case):
//
//	repository, teams, rulesets, environments, webhooks, autolinks,
//	actions, security, pages, secrets, variables, deploy_keys,
//	custom_properties, collaborators, branches
//
// "repository" covers both repo settings and topics, since they ride
// the same Phase A lane.
type DisabledResources map[string]bool

// AllResourceKeys lists every canonical key DisabledResources accepts.
// Source of truth for validators and CLI help.
var AllResourceKeys = []string{
	"repository",
	"teams",
	"rulesets",
	"environments",
	"webhooks",
	"autolinks",
	"actions",
	"security",
	"pages",
	"secrets",
	"variables",
	"deploy_keys",
	"custom_properties",
	"collaborators",
	"branches",
}

// ParseDisabledResources reads a comma-separated list ("pages,secrets,
// deploy-keys") into a DisabledResources set. Whitespace is trimmed,
// case is folded to lower, and hyphens are normalized to underscores
// so "deploy-keys" and "deploy_keys" both land at the canonical key.
//
// Unknown keys are returned in the second value so the caller (main)
// can warn the operator at boot — silently ignoring a typo would
// defeat the point of the compliance gate.
func ParseDisabledResources(csv string) (DisabledResources, []string) {
	if strings.TrimSpace(csv) == "" {
		return DisabledResources{}, nil
	}
	known := map[string]struct{}{}
	for _, k := range AllResourceKeys {
		known[k] = struct{}{}
	}
	out := DisabledResources{}
	var unknown []string
	for _, raw := range strings.Split(csv, ",") {
		key := normalizeResourceKey(raw)
		if key == "" {
			continue
		}
		if _, ok := known[key]; !ok {
			unknown = append(unknown, raw)
			continue
		}
		out[key] = true
	}
	return out, unknown
}

// Has reports whether the given canonical key has been disabled.
// Safe on a nil receiver so callers don't need to nil-check.
func (d DisabledResources) Has(key string) bool {
	if d == nil {
		return false
	}
	return d[key]
}

// Keys returns the disabled keys in stable (insertion-independent)
// order so log lines and PR comments don't shuffle on rerun.
func (d DisabledResources) Keys() []string {
	if len(d) == 0 {
		return nil
	}
	out := make([]string, 0, len(d))
	for _, k := range AllResourceKeys {
		if d[k] {
			out = append(out, k)
		}
	}
	return out
}

// IsResourceConfigured reports whether the given Settings actually
// references the resource. Used by /api/validate so we only warn
// about disabled resources the user is *trying* to use; an empty
// secrets section in disabled mode is a noop, no warning needed.
func IsResourceConfigured(s *Settings, key string) bool {
	if s == nil {
		return false
	}
	switch key {
	case "repository":
		return s.Repo != nil || s.Topics != nil
	case "teams":
		return s.Teams != nil
	case "rulesets":
		return s.Rulesets != nil
	case "environments":
		return s.Environments != nil
	case "webhooks":
		return s.Webhooks != nil
	case "autolinks":
		return s.Autolinks != nil
	case "actions":
		return s.Actions != nil
	case "security":
		return s.Security != nil
	case "pages":
		return s.Pages != nil
	case "secrets":
		return s.Secrets != nil
	case "variables":
		return s.Variables != nil
	case "deploy_keys":
		return s.DeployKeys != nil
	case "custom_properties":
		return s.CustomProperties != nil
	case "collaborators":
		return s.Collaborators != nil
	case "branches":
		return s.Branches != nil
	}
	return false
}

func normalizeResourceKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

// ResourceKeysForFile maps a `.github/settings/<name>.yml` filename to
// the canonical Lane.Resource keys it can configure. `repo.yml`
// affects the "repository" lane (which also covers topics, since
// topics ride Phase A together with repo settings).
//
// Used by /api/validate so it can warn on a file that defines an
// operator-disabled section. Returns nil for unknown filenames so
// the caller can treat them as "no warning, no harm."
func ResourceKeysForFile(name string) []string {
	switch name {
	case "repo.yml":
		return []string{"repository"}
	case "teams.yml":
		return []string{"teams"}
	case "rulesets.yml":
		return []string{"rulesets"}
	case "branches.yml":
		return []string{"branches"}
	case "environments.yml":
		return []string{"environments"}
	case "webhooks.yml":
		return []string{"webhooks"}
	case "autolinks.yml":
		return []string{"autolinks"}
	case "actions.yml":
		return []string{"actions"}
	case "security.yml":
		return []string{"security"}
	case "pages.yml":
		return []string{"pages"}
	case "secrets.yml":
		return []string{"secrets"}
	case "variables.yml":
		return []string{"variables"}
	case "deploy-keys.yml":
		return []string{"deploy_keys"}
	case "custom-properties.yml":
		return []string{"custom_properties"}
	case "collaborators.yml":
		return []string{"collaborators"}
	}
	return nil
}
