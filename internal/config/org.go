package config

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Depthmark/repositories-settings/internal/yamlstrict"
)

// Paths inside the organization's admin repository.
//
// The admin repository holds the org-wide defaults, the policy that
// constrains what a repository may override, and any suborg tiers. It
// uses the same file names and envelopes as a repository so an operator
// only has to learn one format.
const (
	// AdminPolicyPath is the org policy file.
	AdminPolicyPath = SettingsDirPrefix + "policy.yml"
	// SuborgIndexPath declares the suborg tiers and what each matches.
	SuborgIndexPath = SettingsDirPrefix + "suborgs.yml"
	// SuborgDirPrefix is where each suborg's own settings files live,
	// under a directory named after the suborg.
	SuborgDirPrefix = SettingsDirPrefix + "suborgs/"
)

// AdminLayer is the organization-wide configuration, read once from the
// admin repository and reused across every repository in the org.
type AdminLayer struct {
	// Settings are the org-wide defaults every repository inherits.
	Settings *Settings
	// Policy constrains what a repository is allowed to override.
	Policy AdminPolicy
	// Suborgs are override tiers applied between org and repo, in
	// declaration order, to the repositories each one matches.
	Suborgs []Suborg
}

// Suborg is one override tier between the org layer and the repository.
type Suborg struct {
	Name     string
	Match    SuborgMatch
	Settings *Settings
}

// SuborgIndexFile is the YAML at SuborgIndexPath.
type SuborgIndexFile struct {
	Version int           `yaml:"_version"`
	Suborgs []SuborgEntry `yaml:"suborgs"`
}

// SuborgEntry names one suborg and the repositories it applies to. Its
// settings are read from SuborgDirPrefix + Name.
type SuborgEntry struct {
	Name  string      `yaml:"name"`
	Match SuborgMatch `yaml:"match"`
}

// AdminFetcher distinguishes a missing optional file from an inaccessible
// configured repository, both of which GitHub otherwise reports as HTTP 404.
type AdminFetcher interface {
	FileFetcher
	CheckRepository(ctx context.Context, repo Repo) error
}

// LoadAdmin reads the organization layer from adminRepo at ref.
//
// Every part is optional. An admin repository with no policy.yml is a
// pure defaults layer; one with only policy.yml constrains repositories
// without contributing settings. A readable repository with none of these
// files yields an empty layer. A missing or inaccessible configured admin
// repository is an error so a permissions mistake cannot disable policy.
func LoadAdmin(ctx context.Context, f AdminFetcher, adminRepo Repo, ref string) (*AdminLayer, error) {
	if err := f.CheckRepository(ctx, adminRepo); err != nil {
		return nil, fmt.Errorf("access org admin repository %s: %w", adminRepo, err)
	}
	settings, err := loadSettingsDir(ctx, f, adminRepo, SettingsDirPrefix, ref)
	if err != nil {
		return nil, fmt.Errorf("org settings in %s: %w", adminRepo, err)
	}
	layer := &AdminLayer{Settings: settings}

	policy, err := loadAdminPolicy(ctx, f, adminRepo, ref)
	if err != nil {
		return nil, err
	}
	layer.Policy = policy

	suborgs, err := loadSuborgs(ctx, f, adminRepo, ref)
	if err != nil {
		return nil, err
	}
	layer.Suborgs = suborgs

	return layer, nil
}

func loadAdminPolicy(ctx context.Context, f FileFetcher, adminRepo Repo, ref string) (AdminPolicy, error) {
	raw, err := f.GetFile(ctx, adminRepo, AdminPolicyPath, ref)
	if errors.Is(err, ErrNotFound) {
		return AdminPolicy{}, nil
	}
	if err != nil {
		return AdminPolicy{}, fmt.Errorf("%s: %w", AdminPolicyPath, err)
	}
	var file PolicyFile
	if err := yamlstrict.Decode(raw, &file); err != nil {
		return AdminPolicy{}, &ValidationError{File: AdminPolicyPath, Issues: []string{err.Error()}}
	}
	return file.Policy, nil
}

func loadSuborgs(ctx context.Context, f FileFetcher, adminRepo Repo, ref string) ([]Suborg, error) {
	raw, err := f.GetFile(ctx, adminRepo, SuborgIndexPath, ref)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", SuborgIndexPath, err)
	}
	var index SuborgIndexFile
	if err := yamlstrict.Decode(raw, &index); err != nil {
		return nil, &ValidationError{File: SuborgIndexPath, Issues: []string{err.Error()}}
	}

	var issues []string
	seen := make(map[string]bool, len(index.Suborgs))
	out := make([]Suborg, 0, len(index.Suborgs))
	for i, entry := range index.Suborgs {
		name := strings.TrimSpace(entry.Name)
		switch {
		case name == "":
			issues = append(issues, fmt.Sprintf("suborgs[%d]: name is required", i))
			continue
		// The name becomes a path segment; a traversal in it would read
		// files from outside the suborg tree.
		case name != path.Base(name) || name == "." || name == "..":
			issues = append(issues, fmt.Sprintf("suborgs[%d]: name %q must be a single path segment", i, entry.Name))
			continue
		case seen[name]:
			issues = append(issues, fmt.Sprintf("suborgs[%d]: duplicate name %q", i, name))
			continue
		}
		seen[name] = true

		settings, err := loadSettingsDir(ctx, f, adminRepo, SuborgDirPrefix+name+"/", ref)
		if err != nil {
			return nil, fmt.Errorf("suborg %q: %w", name, err)
		}
		out = append(out, Suborg{Name: name, Match: entry.Match, Settings: settings})
	}
	if len(issues) > 0 {
		return nil, &ValidationError{File: SuborgIndexPath, Issues: issues}
	}
	return out, nil
}
