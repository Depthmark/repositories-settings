package applier

import (
	"context"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Autolinks are keyed by key_prefix. GitHub has no update endpoint, so
// changing one is a delete followed by a create.
func NewAutolinksLane(cfg *config.AutolinksConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "autolinks",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			byPrefix := make(map[string]config.Autolink, len(cfg.Autolinks))
			for _, a := range cfg.Autolinks {
				byPrefix[a.KeyPrefix] = a
			}

			fetch := func(ctx context.Context) ([]map[string]any, error) {
				items, err := listAll(ctx, cl, repo, ghapi.RouteAutolinks,
					func(ctx context.Context, opts *github.ListOptions) ([]*github.Autolink, *github.Response, error) {
						return cl.GH().Repositories.ListAutolinks(ctx, repo.Owner, repo.Name, opts)
					})
				if err != nil {
					return nil, err
				}
				return mapAll(items, ghapi.DecodeAutolink), nil
			}

			desired, err := diff.ToMaps(cfg.Autolinks)
			if err != nil {
				return nil, nil, err
			}

			create := func(ctx context.Context, prefix string) error {
				_, _, err := cl.GH().Repositories.AddAutolink(
					writeCtx(ctx, repo, ghapi.RouteAutolinks), repo.Owner, repo.Name,
					ghapi.EncodeAutolink(byPrefix[prefix]))
				return err
			}
			remove := func(ctx context.Context, id int64) error {
				_, err := cl.GH().Repositories.DeleteAutolink(
					writeCtx(ctx, repo, ghapi.RouteAutolinksID), repo.Owner, repo.Name, id)
				return err
			}

			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				prefix := keyOf(d, "key_prefix")
				switch d.Action {
				case diff.Create:
					return Created, create(ctx, prefix)
				case diff.Update:
					if err := remove(ctx, int64(intField(d.Current, "_id"))); err != nil {
						return Updated, err
					}
					return Updated, create(ctx, prefix)
				case diff.Delete:
					return Deleted, remove(ctx, int64(intField(d.Current, "_id")))
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "autolinks", "key_prefix", fetch, desired, mutate, dryRun)
		},
	}
}
