package applier

import (
	"context"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Webhooks are keyed by delivery URL. GitHub nests the transport
// settings under `config` and spells insecure_ssl as "0"/"1", so the
// live shape and the configured shape differ; ghapi owns both
// conversions.
func NewWebhooksLane(cfg *config.WebhooksConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "webhooks",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			byURL := make(map[string]config.Webhook, len(cfg.Webhooks))
			for _, w := range cfg.Webhooks {
				byURL[w.URL] = w
			}

			fetch := func(ctx context.Context) ([]map[string]any, error) {
				hooks, err := listAll(ctx, cl, repo, ghapi.RouteHooks,
					func(ctx context.Context, opts *github.ListOptions) ([]*github.Hook, *github.Response, error) {
						return cl.GH().Repositories.ListHooks(ctx, repo.Owner, repo.Name, opts)
					})
				if err != nil {
					return nil, err
				}
				return mapAll(hooks, ghapi.DecodeHook), nil
			}

			desired, err := diff.ToMaps(cfg.Webhooks)
			if err != nil {
				return nil, nil, err
			}

			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				url := keyOf(d, "url")
				switch d.Action {
				case diff.Create:
					wctx := ghclient.WithCall(ctx, ghclient.PriorityMergeApply, repo.Owner, ghapi.RouteHooks)
					_, _, err := cl.GH().Repositories.CreateHook(wctx, repo.Owner, repo.Name, ghapi.EncodeHook(byURL[url]))
					return Created, err
				case diff.Update:
					wctx := ghclient.WithCall(ctx, ghclient.PriorityMergeApply, repo.Owner, ghapi.RouteHooksID)
					_, _, err := cl.GH().Repositories.EditHook(wctx, repo.Owner, repo.Name,
						int64(intField(d.Current, "_id")), ghapi.EncodeHook(byURL[url]))
					return Updated, err
				case diff.Delete:
					wctx := ghclient.WithCall(ctx, ghclient.PriorityMergeApply, repo.Owner, ghapi.RouteHooksID)
					_, err := cl.GH().Repositories.DeleteHook(wctx, repo.Owner, repo.Name, int64(intField(d.Current, "_id")))
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "webhooks", "url", fetch, desired, mutate, dryRun)
		},
	}
}
