package config

// Merge folds `override` into `base` using last-write-wins precedence at
// the resource granularity. A non-nil pointer in override replaces the
// matching pointer in base; a nil pointer leaves base alone.
//
// Both inputs may be nil. The returned Settings is always non-nil.
func Merge(base, override *Settings) *Settings {
	if base == nil {
		base = &Settings{}
	}
	if override == nil {
		return cloneShallow(base)
	}
	out := cloneShallow(base)
	if override.Repo != nil {
		out.Repo = override.Repo
	}
	if override.Topics != nil {
		out.Topics = override.Topics
	}
	if override.Teams != nil {
		out.Teams = override.Teams
	}
	if override.Rulesets != nil {
		out.Rulesets = override.Rulesets
	}
	if override.Branches != nil {
		out.Branches = override.Branches
	}
	if override.Environments != nil {
		out.Environments = override.Environments
	}
	if override.Webhooks != nil {
		out.Webhooks = override.Webhooks
	}
	if override.Autolinks != nil {
		out.Autolinks = override.Autolinks
	}
	if override.Actions != nil {
		out.Actions = override.Actions
	}
	if override.Security != nil {
		out.Security = override.Security
	}
	if override.Pages != nil {
		out.Pages = override.Pages
	}
	if override.Secrets != nil {
		out.Secrets = override.Secrets
	}
	if override.Variables != nil {
		out.Variables = override.Variables
	}
	if override.DeployKeys != nil {
		out.DeployKeys = override.DeployKeys
	}
	if override.CustomProperties != nil {
		out.CustomProperties = override.CustomProperties
	}
	if override.Collaborators != nil {
		out.Collaborators = override.Collaborators
	}
	return out
}

func cloneShallow(s *Settings) *Settings {
	if s == nil {
		return &Settings{}
	}
	c := *s
	return &c
}

// Provenance tracks where each resource came from after a multi-layer
// merge. Layers are applied in order; the last layer that sets a resource
// wins.
type Provenance map[string]string

// Layer represents one tier of configuration. Order matters: org first,
// then any number of suborgs (in match order), then repo last.
type Layer struct {
	Source   string
	Settings *Settings
}

// Resolve folds layers into a final Settings while recording where each
// resource originated. Used to drive policy validation (which needs to
// know which fields the *repo* set).
func Resolve(layers ...Layer) (*Settings, Provenance) {
	out := &Settings{}
	prov := Provenance{}
	for _, layer := range layers {
		if layer.Settings == nil {
			continue
		}
		s := layer.Settings
		if s.Repo != nil {
			out.Repo = s.Repo
			prov["repo"] = layer.Source
		}
		if s.Topics != nil {
			out.Topics = s.Topics
			prov["topics"] = layer.Source
		}
		if s.Teams != nil {
			out.Teams = s.Teams
			prov["teams"] = layer.Source
		}
		if s.Rulesets != nil {
			out.Rulesets = s.Rulesets
			prov["rulesets"] = layer.Source
		}
		if s.Branches != nil {
			out.Branches = s.Branches
			prov["branches"] = layer.Source
		}
		if s.Environments != nil {
			out.Environments = s.Environments
			prov["environments"] = layer.Source
		}
		if s.Webhooks != nil {
			out.Webhooks = s.Webhooks
			prov["webhooks"] = layer.Source
		}
		if s.Autolinks != nil {
			out.Autolinks = s.Autolinks
			prov["autolinks"] = layer.Source
		}
		if s.Actions != nil {
			out.Actions = s.Actions
			prov["actions"] = layer.Source
		}
		if s.Security != nil {
			out.Security = s.Security
			prov["security"] = layer.Source
		}
		if s.Pages != nil {
			out.Pages = s.Pages
			prov["pages"] = layer.Source
		}
		if s.Secrets != nil {
			out.Secrets = s.Secrets
			prov["secrets"] = layer.Source
		}
		if s.Variables != nil {
			out.Variables = s.Variables
			prov["variables"] = layer.Source
		}
		if s.DeployKeys != nil {
			out.DeployKeys = s.DeployKeys
			prov["deployKeys"] = layer.Source
		}
		if s.CustomProperties != nil {
			out.CustomProperties = s.CustomProperties
			prov["customProperties"] = layer.Source
		}
		if s.Collaborators != nil {
			out.Collaborators = s.Collaborators
			prov["collaborators"] = layer.Source
		}
	}
	return out, prov
}
