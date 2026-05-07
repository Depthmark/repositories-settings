package config

import (
	"reflect"
)

// SuborgMatch describes which repos a suborg overrides apply to. Empty
// criteria match nothing. Multiple non-empty criteria AND together.
type SuborgMatch struct {
	Repos            []string       `yaml:"repos,omitempty" json:"repos,omitempty"`
	Teams            []string       `yaml:"teams,omitempty" json:"teams,omitempty"`
	CustomProperties map[string]any `yaml:"custom_properties,omitempty" json:"custom_properties,omitempty"`
}

// SuborgRepoFile is the YAML at <suborg>/repos.yml.
type SuborgRepoFile struct {
	Version int         `yaml:"_version"`
	Match   SuborgMatch `yaml:"match"`
}

// RepoContext supplies attributes a SuborgMatch can test against.
type RepoContext struct {
	Repo             Repo
	Teams            []string
	CustomProperties map[string]any
}

// Matches returns true if all non-empty criteria match the context.
func (m SuborgMatch) Matches(ctx RepoContext) bool {
	if len(m.Repos) == 0 && len(m.Teams) == 0 && len(m.CustomProperties) == 0 {
		return false
	}
	if len(m.Repos) > 0 {
		if !anyMatch(m.Repos, func(p string) bool {
			return globMatch(ctx.Repo.Name, p) || globMatch(ctx.Repo.String(), p)
		}) {
			return false
		}
	}
	if len(m.Teams) > 0 {
		if !anyMatch(ctx.Teams, func(t string) bool {
			return contains(m.Teams, t)
		}) {
			return false
		}
	}
	if len(m.CustomProperties) > 0 {
		for k, want := range m.CustomProperties {
			got, ok := ctx.CustomProperties[k]
			if !ok || !reflect.DeepEqual(got, want) {
				return false
			}
		}
	}
	return true
}

func anyMatch[T any](items []T, pred func(T) bool) bool {
	for _, it := range items {
		if pred(it) {
			return true
		}
	}
	return false
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// globMatch implements a small glob: `*` matches any chars except `/`,
// `**` matches any chars including `/`, `?` matches a single non-slash.
// Case-insensitive — matches the TS regex policy.
func globMatch(text, pattern string) bool {
	return globRegex(pattern).MatchString(text)
}
