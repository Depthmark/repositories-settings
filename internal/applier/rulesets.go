package applier

import (
	"context"
	"net/http"

	"golang.org/x/sync/errgroup"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// rulesetsDetailConcurrency caps in-flight `GET /rulesets/{id}` calls.
// Five is enough to collapse wall-clock for typical repo sizes (1–20
// rulesets) without crowding the REST pool when many lanes run concurrently.
const rulesetsDetailConcurrency = 5

func NewRulesetsLane(cfg *config.RulesetsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "rulesets",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				// List ruleset IDs over GraphQL (1 call, GraphQL pool)
				// instead of REST list (1 call, REST pool). The detail
				// fetches still need REST — GraphQL's RepositoryRule
				// parameters union doesn't round-trip cleanly to the
				// REST shape that the diff compares against — but they
				// now run in parallel, so wall-clock is ~RTT regardless
				// of ruleset count.
				items, err := cl.FetchRepoRulesetIDs(ctx, ghclient.PriorityCronReconcile, repo)
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, len(items))
				g, gctx := errgroup.WithContext(ctx)
				g.SetLimit(rulesetsDetailConcurrency)
				for i, it := range items {
					g.Go(func() error {
						var full map[string]any
						_, err := cl.Do(gctx, ghclient.Call{
							Prio: ghclient.PriorityCronReconcile, Owner: repo.Owner, Method: http.MethodGet,
							Path: ghapi.RulesetsID(repo, it.ID), Route: ghapi.RouteRulesetsID, Out: &full,
						})
						if err != nil {
							return err
						}
						full["_id"] = it.ID
						full["name"] = it.Name
						out[i] = full
						return nil
					})
				}
				if err := g.Wait(); err != nil {
					return nil, err
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
					_, err := cl.Do(ctx, ghclient.Call{
						Prio: ghclient.PriorityMergeApply, Owner: repo.Owner, Method: http.MethodPost,
						Path: ghapi.Rulesets(repo), Route: ghapi.RouteRulesets,
						Body: map[string]any(ghapi.RulesetBody(d.Desired)),
					})
					return Created, err
				case diff.Update:
					_, err := cl.Do(ctx, ghclient.Call{
						Prio: ghclient.PriorityMergeApply, Owner: repo.Owner, Method: http.MethodPut,
						Path: ghapi.RulesetsID(repo, intField(d.Current, "_id")), Route: ghapi.RouteRulesetsID,
						Body: map[string]any(ghapi.RulesetBody(d.Desired)),
					})
					return Updated, err
				case diff.Delete:
					_, err := cl.Do(ctx, ghclient.Call{
						Prio: ghclient.PriorityMergeApply, Owner: repo.Owner, Method: http.MethodDelete,
						Path: ghapi.RulesetsID(repo, intField(d.Current, "_id")), Route: ghapi.RouteRulesetsID,
					})
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "rulesets", "name", fetch, desired, mutate, dryRun)
		},
	}
}
