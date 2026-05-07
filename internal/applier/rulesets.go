package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

func NewRulesetsLane(cfg *config.RulesetsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "rulesets",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveRule struct {
					ID   int    `json:"id"`
					Name string `json:"name"`
				}
				items, err := ghclient.Paginate[liveRule](ctx, cl, ghclient.PriorityCronReconcile, repo.Owner,
					fmt.Sprintf("/repos/%s/%s/rulesets", repo.Owner, repo.Name))
				if err != nil {
					return nil, err
				}
				// Fetch each ruleset's full state in parallel.
				type fullRuleset map[string]any
				out := make([]map[string]any, len(items))
				for i, it := range items {
					var full fullRuleset
					_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
						fmt.Sprintf("/repos/%s/%s/rulesets/%d", repo.Owner, repo.Name, it.ID), nil, &full)
					if err != nil {
						return nil, err
					}
					full["_id"] = it.ID
					full["name"] = it.Name
					out[i] = full
				}
				return out, nil
			}
			desired, err := diff.ToMaps(cfg.Rulesets)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				switch d.Action {
				case diff.Create:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPost,
						fmt.Sprintf("/repos/%s/%s/rulesets", repo.Owner, repo.Name), d.Desired, nil)
					return Created, err
				case diff.Update:
					id := intField(d.Current, "_id")
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPut,
						fmt.Sprintf("/repos/%s/%s/rulesets/%d", repo.Owner, repo.Name, int(id)), d.Desired, nil)
					return Updated, err
				case diff.Delete:
					id := intField(d.Current, "_id")
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
						fmt.Sprintf("/repos/%s/%s/rulesets/%d", repo.Owner, repo.Name, int(id)), nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "rulesets", "name", fetch, desired, mutate, dryRun)
		},
	}
}
