package applier

import (
	"context"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Teams are read from the repository but written on the org: binding a
// team to a repo is PUT /orgs/{org}/teams/{slug}/repos/{owner}/{repo}.
func NewTeamsLane(cfg *config.TeamsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "teams",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				teams, err := listAll(ctx, cl, repo, ghapi.RouteRepoTeams,
					func(ctx context.Context, opts *github.ListOptions) ([]*github.Team, *github.Response, error) {
						return cl.GH().Repositories.ListTeams(ctx, repo.Owner, repo.Name, opts)
					})
				if err != nil {
					return nil, err
				}
				return mapAll(teams, ghapi.DecodeTeam), nil
			}

			desired, err := diff.ToMaps(cfg.Teams)
			if err != nil {
				return nil, nil, err
			}

			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				slug := keyOf(d, "slug")
				wctx := writeCtx(ctx, repo, ghapi.RouteOrgTeamRepo)
				switch d.Action {
				case diff.Create, diff.Update:
					_, err := cl.GH().Teams.AddTeamRepoBySlug(wctx, repo.Owner, slug, repo.Owner, repo.Name,
						&github.TeamAddTeamRepoOptions{Permission: stringField(d.Desired, "permission")})
					if d.Action == diff.Create {
						return Created, err
					}
					return Updated, err
				case diff.Delete:
					_, err := cl.GH().Teams.RemoveTeamRepoBySlug(wctx, repo.Owner, slug, repo.Owner, repo.Name)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "teams", "slug", fetch, desired, mutate, dryRun)
		},
	}
}
