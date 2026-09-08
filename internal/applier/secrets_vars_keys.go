package applier

import (
	"context"
	"fmt"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Secrets: only names are managed (values come from a separate secret
// store). Live state lists names; create == note that secret needs
// provisioning, delete == DELETE.
func NewSecretsLane(cfg *config.SecretsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "secrets",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				secrets, err := listAll(ctx, cl, repo, ghapi.RouteSecrets,
					func(ctx context.Context, opts *github.ListOptions) ([]*github.Secret, *github.Response, error) {
						page, resp, err := cl.GH().Actions.ListRepoSecrets(ctx, repo.Owner, repo.Name, opts)
						if err != nil {
							return nil, resp, err
						}
						return page.Secrets, resp, nil
					})
				if err != nil {
					return nil, err
				}
				return mapAll(secrets, ghapi.DecodeSecret), nil
			}
			desired, err := diff.ToMaps(cfg.RepositorySecrets)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				name := stringField(d.Desired, "name")
				if name == "" {
					name = stringField(d.Current, "name")
				}
				switch d.Action {
				case diff.Create:
					// A declared secret with no value in GitHub yet is
					// the normal steady state, not a failure: the value
					// comes from a separate store, and this service only
					// manages the name. Reporting it as an error made
					// every PR check on such a repo go red forever.
					return Pending, fmt.Errorf("secret %q is declared here but its value has not been uploaded to GitHub — set it in the repository's Actions secrets, or remove it from secrets.yml", name)
				case diff.Delete:
					_, err := cl.GH().Actions.DeleteRepoSecret(
						writeCtx(ctx, repo, ghapi.RouteSecretsName), repo.Owner, repo.Name, name)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "secrets", "name", fetch, desired, mutate, dryRun)
		},
	}
}

func NewVariablesLane(cfg *config.VariablesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "variables",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				vars, err := listAll(ctx, cl, repo, ghapi.RouteVariables,
					func(ctx context.Context, opts *github.ListOptions) ([]*github.ActionsVariable, *github.Response, error) {
						page, resp, err := cl.GH().Actions.ListRepoVariables(ctx, repo.Owner, repo.Name, opts)
						if err != nil {
							return nil, resp, err
						}
						return page.Variables, resp, nil
					})
				if err != nil {
					return nil, err
				}
				return mapAll(vars, ghapi.DecodeVariable), nil
			}
			desired, err := diff.ToMaps(cfg.Variables)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				name := stringField(d.Desired, "name")
				if name == "" {
					name = stringField(d.Current, "name")
				}
				v := &github.ActionsVariable{Name: name, Value: stringField(d.Desired, "value")}
				switch d.Action {
				case diff.Create:
					_, err := cl.GH().Actions.CreateRepoVariable(
						writeCtx(ctx, repo, ghapi.RouteVariables), repo.Owner, repo.Name, v)
					return Created, err
				case diff.Update:
					_, err := cl.GH().Actions.UpdateRepoVariable(
						writeCtx(ctx, repo, ghapi.RouteVariablesName), repo.Owner, repo.Name, v)
					return Updated, err
				case diff.Delete:
					_, err := cl.GH().Actions.DeleteRepoVariable(
						writeCtx(ctx, repo, ghapi.RouteVariablesName), repo.Owner, repo.Name, name)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "variables", "name", fetch, desired, mutate, dryRun)
		},
	}
}

// Deploy keys: GitHub only supports create + delete. Update == replace.
func NewDeployKeysLane(cfg *config.DeployKeysConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "deploy_keys",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				keys, err := listAll(ctx, cl, repo, ghapi.RouteKeys,
					func(ctx context.Context, opts *github.ListOptions) ([]*github.Key, *github.Response, error) {
						return cl.GH().Repositories.ListKeys(ctx, repo.Owner, repo.Name, opts)
					})
				if err != nil {
					return nil, err
				}
				return mapAll(keys, ghapi.DecodeKey), nil
			}
			// The key material never reaches the diff document: GitHub
			// does not return it, so it could never compare equal. It
			// is looked up by title at mutate time instead.
			items, err := diff.ToMaps(cfg.DeployKeys)
			if err != nil {
				return nil, nil, err
			}
			material := make(map[string]config.DeployKey, len(cfg.DeployKeys))
			for _, k := range cfg.DeployKeys {
				material[k.Title] = k
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				title := stringField(d.Desired, "title")
				if title == "" {
					title = stringField(d.Current, "title")
				}
				remove := func(id int64) error {
					_, err := cl.GH().Repositories.DeleteKey(
						writeCtx(ctx, repo, ghapi.RouteKeysID), repo.Owner, repo.Name, id)
					return err
				}
				switch d.Action {
				case diff.Create, diff.Update:
					k := material[title]
					if k.Key == "" {
						// Same shape as an unprovisioned secret: the
						// config names a key whose material lives
						// elsewhere. Nothing to do, and not an error.
						return Pending, fmt.Errorf("deploy key %q has no `key` value in deploy-keys.yml, so there is nothing to upload", title)
					}
					// GitHub has no PATCH for deploy keys.
					if d.Action == diff.Update {
						if err := remove(int64(intField(d.Current, "_id"))); err != nil {
							return Updated, err
						}
					}
					_, _, err := cl.GH().Repositories.CreateKey(
						writeCtx(ctx, repo, ghapi.RouteKeys), repo.Owner, repo.Name, ghapi.EncodeKey(k))
					if d.Action == diff.Create {
						return Created, err
					}
					return Updated, err
				case diff.Delete:
					return Deleted, remove(int64(intField(d.Current, "_id")))
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "deploy_keys", "title", fetch, items, mutate, dryRun)
		},
	}
}
