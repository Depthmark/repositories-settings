package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Autolinks: GitHub doesn't support PATCH on autolinks, so update == delete + create.
func NewAutolinksLane(cfg *config.AutolinksConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "autolinks",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveAuto struct {
					ID             int    `json:"id"`
					KeyPrefix      string `json:"key_prefix"`
					URLTemplate    string `json:"url_template"`
					IsAlphanumeric bool   `json:"is_alphanumeric"`
				}
				items, err := ghclient.Paginate[liveAuto](ctx, cl, ghclient.PriorityCronReconcile, repo.Owner,
					fmt.Sprintf("/repos/%s/%s/autolinks", repo.Owner, repo.Name))
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(items))
				for _, a := range items {
					out = append(out, map[string]any{
						"_id":             a.ID,
						"key_prefix":      a.KeyPrefix,
						"url_template":    a.URLTemplate,
						"is_alphanumeric": a.IsAlphanumeric,
					})
				}
				return out, nil
			}
			desired, err := diff.ToMaps(cfg.Autolinks)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				switch d.Action {
				case diff.Create:
					payload := map[string]any{
						"key_prefix":      stringField(d.Desired, "key_prefix"),
						"url_template":    stringField(d.Desired, "url_template"),
						"is_alphanumeric": d.Desired.(map[string]any)["is_alphanumeric"],
					}
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPost,
						fmt.Sprintf("/repos/%s/%s/autolinks", repo.Owner, repo.Name), payload, nil)
					return Created, err
				case diff.Update:
					// Delete then re-create.
					id := intField(d.Current, "_id")
					if _, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
						fmt.Sprintf("/repos/%s/%s/autolinks/%d", repo.Owner, repo.Name, int(id)), nil, nil); err != nil {
						return Updated, err
					}
					payload := map[string]any{
						"key_prefix":      stringField(d.Desired, "key_prefix"),
						"url_template":    stringField(d.Desired, "url_template"),
						"is_alphanumeric": d.Desired.(map[string]any)["is_alphanumeric"],
					}
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPost,
						fmt.Sprintf("/repos/%s/%s/autolinks", repo.Owner, repo.Name), payload, nil)
					return Updated, err
				case diff.Delete:
					id := intField(d.Current, "_id")
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
						fmt.Sprintf("/repos/%s/%s/autolinks/%d", repo.Owner, repo.Name, int(id)), nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "autolinks", "key_prefix", fetch, desired, mutate, dryRun)
		},
	}
}
