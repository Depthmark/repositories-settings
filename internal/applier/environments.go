package applier

import (
	"context"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Environments are keyed by name. GitHub reports an environment's
// settings as a `protection_rules` array of tagged objects and accepts
// them as flat fields on the update body, so live state has to be
// projected onto the configured shape before diffing — see
// ghapi.DecodeEnvironment.
func NewEnvironmentsLane(cfg *config.EnvironmentsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "environments",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			byName := make(map[string]config.Environment, len(cfg.Environments))
			for _, e := range cfg.Environments {
				byName[e.Name] = e
			}

			fetch := func(ctx context.Context) ([]map[string]any, error) {
				var out []*github.Environment
				opts := &github.EnvironmentListOptions{ListOptions: github.ListOptions{PerPage: 100}}
				for {
					page, resp, err := cl.GH().Repositories.ListEnvironments(
						readCtx(ctx, repo, ghapi.RouteEnvironments), repo.Owner, repo.Name, opts)
					if err != nil {
						return nil, err
					}
					if page != nil {
						out = append(out, page.Environments...)
					}
					if resp == nil || resp.NextPage == 0 {
						break
					}
					opts.Page = resp.NextPage
				}
				return mapAll(out, ghapi.DecodeEnvironment), nil
			}

			desired, err := diff.ToMaps(cfg.Environments)
			if err != nil {
				return nil, nil, err
			}

			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				name := keyOf(d, "name")
				wctx := writeCtx(ctx, repo, ghapi.RouteEnvironmentsName)
				switch d.Action {
				case diff.Create, diff.Update:
					_, _, err := cl.GH().Repositories.CreateUpdateEnvironment(
						wctx, repo.Owner, repo.Name, name, ghapi.EncodeEnvironment(byName[name]))
					if d.Action == diff.Create {
						return Created, err
					}
					return Updated, err
				case diff.Delete:
					_, err := cl.GH().Repositories.DeleteEnvironment(wctx, repo.Owner, repo.Name, name)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "environments", "name", fetch, desired, mutate, dryRun)
		},
	}
}
