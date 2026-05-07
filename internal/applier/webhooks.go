package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

func NewWebhooksLane(cfg *config.WebhooksConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "webhooks",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveHook struct {
					ID     int      `json:"id"`
					Active bool     `json:"active"`
					Events []string `json:"events"`
					Config struct {
						URL         string `json:"url"`
						ContentType string `json:"content_type"`
						InsecureSSL string `json:"insecure_ssl"`
					} `json:"config"`
				}
				hooks, err := ghclient.Paginate[liveHook](ctx, cl, ghclient.PriorityCronReconcile, repo.Owner,
					fmt.Sprintf("/repos/%s/%s/hooks", repo.Owner, repo.Name))
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(hooks))
				for _, h := range hooks {
					out = append(out, map[string]any{
						"_id":          h.ID,
						"url":          h.Config.URL,
						"content_type": h.Config.ContentType,
						"insecure_ssl": h.Config.InsecureSSL == "1",
						"active":       h.Active,
						"events":       h.Events,
					})
				}
				return out, nil
			}
			desired, err := diff.ToMaps(cfg.Webhooks)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				switch d.Action {
				case diff.Create:
					payload := buildHookPayload(d.Desired)
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPost,
						fmt.Sprintf("/repos/%s/%s/hooks", repo.Owner, repo.Name), payload, nil)
					return Created, err
				case diff.Update:
					id := intField(d.Current, "_id")
					payload := buildHookPayload(d.Desired)
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPatch,
						fmt.Sprintf("/repos/%s/%s/hooks/%d", repo.Owner, repo.Name, int(id)), payload, nil)
					return Updated, err
				case diff.Delete:
					id := intField(d.Current, "_id")
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
						fmt.Sprintf("/repos/%s/%s/hooks/%d", repo.Owner, repo.Name, int(id)), nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "webhooks", "url", fetch, desired, mutate, dryRun)
		},
	}
}

func buildHookPayload(desired any) map[string]any {
	m, _ := desired.(map[string]any)
	cfg := map[string]any{
		"url":          m["url"],
		"content_type": m["content_type"],
	}
	if v, ok := m["insecure_ssl"].(bool); ok {
		if v {
			cfg["insecure_ssl"] = "1"
		} else {
			cfg["insecure_ssl"] = "0"
		}
	}
	return map[string]any{
		"name":   "web",
		"active": m["active"],
		"events": m["events"],
		"config": cfg,
	}
}
