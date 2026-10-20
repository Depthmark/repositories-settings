package applier

import (
	"context"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Collaborators covers direct grants only. Access inherited from org
// membership or a team is not this resource's to manage, and listing it
// would make every reconcile try to revoke it.
func NewCollaboratorsLane(cfg *config.CollaboratorsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "collaborators",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				var out []*github.User
				opts := &github.ListCollaboratorsOptions{
					Affiliation: "direct",
					ListOptions: github.ListOptions{PerPage: 100},
				}
				for {
					users, resp, err := cl.GH().Repositories.ListCollaborators(
						readCtx(ctx, repo, ghapi.RouteCollaborators), repo.Owner, repo.Name, opts)
					if err != nil {
						return nil, err
					}
					out = append(out, users...)
					if resp == nil || resp.NextPage == 0 {
						break
					}
					opts.Page = resp.NextPage
				}
				return mapAll(out, ghapi.DecodeCollaborator), nil
			}

			desired, err := diff.ToMaps(cfg.Collaborators)
			if err != nil {
				return nil, nil, err
			}

			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				user := keyOf(d, "username")
				wctx := writeCtx(ctx, repo, ghapi.RouteCollaboratorsUser)
				switch d.Action {
				case diff.Create, diff.Update:
					_, _, err := cl.GH().Repositories.AddCollaborator(wctx, repo.Owner, repo.Name, user,
						&github.RepositoryAddCollaboratorOptions{Permission: stringField(d.Desired, "permission")})
					if d.Action == diff.Create {
						return Created, err
					}
					return Updated, err
				case diff.Delete:
					_, err := cl.GH().Repositories.RemoveCollaborator(wctx, repo.Owner, repo.Name, user)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "collaborators", "username", fetch, desired, mutate, dryRun)
		},
	}
}
