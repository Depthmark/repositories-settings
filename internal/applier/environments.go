package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

func NewEnvironmentsLane(cfg *config.EnvironmentsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "environments",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveEnv struct {
					Environments []map[string]any `json:"environments"`
				}
				var le liveEnv
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s/environments?per_page=100", repo.Owner, repo.Name), nil, &le)
				if err != nil {
					return nil, err
				}
				return le.Environments, nil
			}
			desired, err := diff.ToMaps(cfg.Environments)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				name := stringField(d.Desired, "name")
				if name == "" {
					name = stringField(d.Current, "name")
				}
				path := fmt.Sprintf("/repos/%s/%s/environments/%s", repo.Owner, repo.Name, name)
				switch d.Action {
				case diff.Create, diff.Update:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPut, path, d.Desired, nil)
					if d.Action == diff.Create {
						return Created, err
					}
					return Updated, err
				case diff.Delete:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete, path, nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "environments", "name", fetch, desired, mutate, dryRun)
		},
	}
}
