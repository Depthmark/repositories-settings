package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/conc"
	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Branches: legacy branch-protection API. Each pattern is a separate
// resource at PUT /repos/:owner/:repo/branches/:branch/protection.
// Live state must be fetched per pattern; absent = unprotected (404).
func NewBranchesLane(cfg *config.BranchesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "branches",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			diffs := make([]diff.Diff, 0, len(cfg.Branches))
			for _, b := range cfg.Branches {
				path := fmt.Sprintf("/repos/%s/%s/branches/%s/protection", repo.Owner, repo.Name, b.Pattern)
				var live map[string]any
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet, path, nil, &live)
				if err != nil && !ghclient.IsNotFound(err) {
					return nil, nil, fmt.Errorf("get branch protection %q: %w", b.Pattern, err)
				}
				desired, _ := diff.ToMap(b)
				delete(desired, "pattern")
				d := diff.SingleResource("branches."+b.Pattern, live, desired)
				diffs = append(diffs, d)
			}
			if dryRun {
				return diffs, nil, nil
			}
			actionable := make([]diff.Diff, 0, len(diffs))
			for _, d := range diffs {
				if d.Action != diff.Noop {
					actionable = append(actionable, d)
				}
			}
			if len(actionable) == 0 {
				return diffs, nil, nil
			}
			results, _ := conc.Map(ctx, actionable, conc.DefaultApplierConcurrency, func(ctx context.Context, d diff.Diff) (Result, error) {
				// Resource format: branches.<pattern>
				pattern := d.Resource[len("branches."):]
				path := fmt.Sprintf("/repos/%s/%s/branches/%s/protection", repo.Owner, repo.Name, pattern)
				switch d.Action {
				case diff.Create, diff.Update:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPut, path, d.Desired, nil)
					if err != nil {
						return failure(d.Resource, Updated, err, 1), nil
					}
					return success(d.Resource, Updated, 1), nil
				case diff.Delete:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete, path, nil, nil)
					if err != nil {
						return failure(d.Resource, Deleted, err, 1), nil
					}
					return success(d.Resource, Deleted, 1), nil
				}
				return success(d.Resource, Skipped, 0), nil
			})
			return diffs, results, nil
		},
	}
}
