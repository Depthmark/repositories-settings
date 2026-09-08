package config

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Depthmark/repositories-settings/internal/yamlstrict"
)

// FileFetcher reads a single file from a repo (returns NotFound if absent).
// Decouples loading from the GitHub client so tests can use an in-memory
// fetcher.
type FileFetcher interface {
	GetFile(ctx context.Context, repo Repo, path, ref string) ([]byte, error)
}

// ErrNotFound signals that the file is absent in the repo. Loader treats
// this as "this resource is unmanaged" — not a load failure.
var ErrNotFound = errors.New("file not found")

// Load reads every settings file in `.github/settings/` and returns the
// merged Settings. Files run in parallel; missing files are skipped; bad
// YAML or schema errors are returned with the file name prefixed.
func Load(ctx context.Context, f FileFetcher, repo Repo, ref string) (*Settings, error) {
	return loadSettingsDir(ctx, f, repo, SettingsDirPrefix, ref)
}

// loadSettingsDir is Load against an arbitrary directory. The org admin
// repository reuses it for the org layer and for each suborg tier, so
// every layer parses through exactly the same validation.
func loadSettingsDir(ctx context.Context, f FileFetcher, repo Repo, dir, ref string) (*Settings, error) {
	type result struct {
		name    string
		content []byte
		err     error
	}

	results := make(chan result, len(FileNames))
	var wg sync.WaitGroup
	for _, name := range FileNames {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			b, err := f.GetFile(ctx, repo, dir+name, ref)
			results <- result{name: name, content: b, err: err}
		}(name)
	}
	go func() { wg.Wait(); close(results) }()

	settings := &Settings{}
	var loadErrs []error
	for r := range results {
		if errors.Is(r.err, ErrNotFound) {
			continue
		}
		if r.err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("%s: %w", r.name, r.err))
			continue
		}
		if err := mergeFile(settings, r.name, r.content); err != nil {
			loadErrs = append(loadErrs, err)
		}
	}
	if len(loadErrs) > 0 {
		return nil, errors.Join(loadErrs...)
	}
	settings.Normalize()
	return settings, nil
}

// LoadFromMap parses a name->content map (used by /api/validate and tests).
func LoadFromMap(files map[string][]byte) (*Settings, error) {
	settings := &Settings{}
	var errs []error
	for name, content := range files {
		if err := mergeFile(settings, name, content); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	settings.Normalize()
	return settings, nil
}

// decodeFile is the shared body of every case in mergeFile: strict
// YAML decode, schema validation, then fold the result into Settings.
// Split out so each case reads as "which envelope, which validator,
// where it lands" rather than eight lines of identical boilerplate.
func decodeFile[F any](name string, raw []byte, validate func(*F) error, apply func(*F)) error {
	var f F
	if err := yamlstrict.Decode(raw, &f); err != nil {
		if errors.Is(err, yamlstrict.ErrEmpty) {
			// An empty file says the same thing as an absent one: this
			// resource is unmanaged. Applying the zero-valued envelope
			// instead would read as "manage this resource, with nothing
			// in it" — i.e. delete everything GitHub currently has.
			return nil
		}
		return &ValidationError{File: name, Issues: yamlstrict.Issues(err)}
	}
	if err := validate(&f); err != nil {
		return err
	}
	apply(&f)
	return nil
}

// mergeFile decodes one file's YAML into the right envelope, validates
// it, and folds it into settings.
func mergeFile(settings *Settings, name string, raw []byte) error {
	switch name {
	case "repo.yml":
		return decodeFile(name, raw, ValidateRepoFile, func(f *RepoFile) {
			if f.Repository != nil {
				settings.Repo = f.Repository
			}
			if f.Topics != nil {
				settings.Topics = f.Topics
			}
		})
	case "teams.yml":
		return decodeFile(name, raw, ValidateTeamsFile, func(f *TeamsFile) {
			settings.Teams = &TeamsConfig{Teams: f.Teams}
		})
	case "rulesets.yml":
		return decodeFile(name, raw, ValidateRulesetsFile, func(f *RulesetsFile) {
			settings.Rulesets = &RulesetsConfig{Rulesets: f.Rulesets}
		})
	case "branches.yml":
		return decodeFile(name, raw, ValidateBranchesFile, func(f *BranchesFile) {
			settings.Branches = &BranchesConfig{Branches: f.Branches}
		})
	case "environments.yml":
		return decodeFile(name, raw, ValidateEnvironmentsFile, func(f *EnvironmentsFile) {
			settings.Environments = &EnvironmentsConfig{Environments: f.Environments}
		})
	case "webhooks.yml":
		return decodeFile(name, raw, ValidateWebhooksFile, func(f *WebhooksFile) {
			settings.Webhooks = &WebhooksConfig{Webhooks: f.Webhooks}
		})
	case "autolinks.yml":
		return decodeFile(name, raw, ValidateAutolinksFile, func(f *AutolinksFile) {
			settings.Autolinks = &AutolinksConfig{Autolinks: f.Autolinks}
		})
	case "actions.yml":
		return decodeFile(name, raw, ValidateActionsFile, func(f *ActionsFile) {
			ac := f.Actions
			settings.Actions = &ac
		})
	case "security.yml":
		return decodeFile(name, raw, ValidateSecurityFile, func(f *SecurityFile) {
			sc := f.Security
			settings.Security = &sc
		})
	case "pages.yml":
		return decodeFile(name, raw, ValidatePagesFile, func(f *PagesFile) {
			pc := f.Pages
			settings.Pages = &pc
		})
	case "secrets.yml":
		return decodeFile(name, raw, ValidateSecretsFile, func(f *SecretsFile) {
			settings.Secrets = &SecretsConfig{RepositorySecrets: f.RepositorySecrets}
		})
	case "variables.yml":
		return decodeFile(name, raw, ValidateVariablesFile, func(f *VariablesFile) {
			settings.Variables = &VariablesConfig{Variables: f.Variables}
		})
	case "deploy-keys.yml":
		return decodeFile(name, raw, ValidateDeployKeysFile, func(f *DeployKeysFile) {
			settings.DeployKeys = &DeployKeysConfig{DeployKeys: f.DeployKeys}
		})
	case "custom-properties.yml":
		return decodeFile(name, raw, ValidateCustomPropertiesFile, func(f *CustomPropertiesFile) {
			settings.CustomProperties = &CustomPropertiesConfig{Properties: f.CustomProperties}
		})
	case "collaborators.yml":
		return decodeFile(name, raw, ValidateCollaboratorsFile, func(f *CollaboratorsFile) {
			settings.Collaborators = &CollaboratorsConfig{Collaborators: f.Collaborators}
		})
	default:
		return &ValidationError{
			File:   name,
			Issues: []string{fmt.Sprintf("unknown settings file %q — see .github/settings/ for the recognised names", name)},
		}
	}
}
