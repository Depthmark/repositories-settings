package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Actions: GET /repos/:owner/:repo/actions/permissions returns enabled,
// allowed_actions. PATCH same path. selected-actions config has its own
// endpoint we don't use here.
func NewActionsLane(cfg *config.ActionsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "actions",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				var live map[string]any
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s/actions/permissions", repo.Owner, repo.Name), nil, &live)
				return live, err
			}
			desired, err := diff.ToMap(cfg)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				patch := map[string]any{}
				for _, c := range d.Changes {
					patch[c.Path] = c.To
				}
				_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPut,
					fmt.Sprintf("/repos/%s/%s/actions/permissions", repo.Owner, repo.Name), patch, nil)
				return Updated, err
			}
			return singleResourceRun(ctx, "actions", fetch, desired, mutate, dryRun)
		},
	}
}

// Security: enable/disable per setting via dedicated endpoints. We treat
// the bag as a single resource; mutate dispatches per field.
func NewSecurityLane(cfg *config.SecurityConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "security",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				// Read the security_and_analysis block from GET /repos/:owner/:repo.
				type repoView struct {
					SecurityAndAnalysis map[string]struct {
						Status string `json:"status"`
					} `json:"security_and_analysis"`
				}
				var rv repoView
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s", repo.Owner, repo.Name), nil, &rv)
				if err != nil {
					return nil, err
				}
				live := map[string]any{}
				for k, v := range rv.SecurityAndAnalysis {
					live[k] = v.Status == "enabled"
				}
				return live, nil
			}
			desired, err := diff.ToMap(cfg)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				// Map each changed field to its endpoint. For simplicity
				// fold all into a single PATCH on the repo settings.
				body := map[string]any{"security_and_analysis": map[string]any{}}
				saa := body["security_and_analysis"].(map[string]any)
				for _, c := range d.Changes {
					status := "disabled"
					if v, ok := c.To.(bool); ok && v {
						status = "enabled"
					}
					saa[c.Path] = map[string]any{"status": status}
				}
				_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPatch,
					fmt.Sprintf("/repos/%s/%s", repo.Owner, repo.Name), body, nil)
				return Updated, err
			}
			return singleResourceRun(ctx, "security", fetch, desired, mutate, dryRun)
		},
	}
}

func NewPagesLane(cfg *config.PagesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "pages",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				var live map[string]any
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s/pages", repo.Owner, repo.Name), nil, &live)
				if err != nil {
					if ghclient.IsNotFound(err) {
						return nil, nil
					}
					return nil, err
				}
				return live, nil
			}
			desired, err := diff.ToMap(cfg)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				method := http.MethodPatch
				if d.Action == diff.Create {
					method = http.MethodPost
				}
				_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, method,
					fmt.Sprintf("/repos/%s/%s/pages", repo.Owner, repo.Name), d.Desired, nil)
				if d.Action == diff.Create {
					return Created, err
				}
				return Updated, err
			}
			return singleResourceRun(ctx, "pages", fetch, desired, mutate, dryRun)
		},
	}
}

func NewCustomPropertiesLane(cfg *config.CustomPropertiesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "custom_properties",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) (map[string]any, error) {
				type liveProp struct {
					PropertyName string `json:"property_name"`
					Value        any    `json:"value"`
				}
				var props []liveProp
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s/properties/values", repo.Owner, repo.Name), nil, &props)
				if err != nil {
					return nil, err
				}
				live := map[string]any{}
				for _, p := range props {
					live[p.PropertyName] = p.Value
				}
				return live, nil
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				// PATCH expects { "properties": [ {property_name, value}, ... ] }
				items := []map[string]any{}
				for k, v := range cfg.Properties {
					items = append(items, map[string]any{"property_name": k, "value": v})
				}
				_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPatch,
					fmt.Sprintf("/repos/%s/%s/properties/values", repo.Owner, repo.Name),
					map[string]any{"properties": items}, nil)
				return Updated, err
			}
			return singleResourceRun(ctx, "custom_properties", fetch, cfg.Properties, mutate, dryRun)
		},
	}
}
