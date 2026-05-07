package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Secrets: only names are managed (values come from a separate secret
// store). Live state lists names; create == note that secret needs
// provisioning, delete == DELETE.
func NewSecretsLane(cfg *config.SecretsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "secrets",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveSecrets struct {
					Secrets []struct {
						Name string `json:"name"`
					} `json:"secrets"`
				}
				var ls liveSecrets
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s/actions/secrets?per_page=100", repo.Owner, repo.Name), nil, &ls)
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(ls.Secrets))
				for _, s := range ls.Secrets {
					out = append(out, map[string]any{"name": s.Name})
				}
				return out, nil
			}
			desired, err := diff.ToMaps(cfg.RepositorySecrets)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				name := stringField(d.Desired, "name")
				if name == "" {
					name = stringField(d.Current, "name")
				}
				switch d.Action {
				case diff.Create:
					// Cannot create without a value. Record as skipped.
					return Skipped, fmt.Errorf("secret %q referenced in config but value not provided", name)
				case diff.Delete:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
						fmt.Sprintf("/repos/%s/%s/actions/secrets/%s", repo.Owner, repo.Name, name), nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "secrets", "name", fetch, desired, mutate, dryRun)
		},
	}
}

func NewVariablesLane(cfg *config.VariablesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "variables",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveVars struct {
					Variables []struct {
						Name  string `json:"name"`
						Value string `json:"value"`
					} `json:"variables"`
				}
				var lv liveVars
				_, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s/actions/variables?per_page=100", repo.Owner, repo.Name), nil, &lv)
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(lv.Variables))
				for _, v := range lv.Variables {
					out = append(out, map[string]any{"name": v.Name, "value": v.Value})
				}
				return out, nil
			}
			desired, err := diff.ToMaps(cfg.Variables)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				name := stringField(d.Desired, "name")
				if name == "" {
					name = stringField(d.Current, "name")
				}
				switch d.Action {
				case diff.Create:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPost,
						fmt.Sprintf("/repos/%s/%s/actions/variables", repo.Owner, repo.Name),
						map[string]any{"name": name, "value": stringField(d.Desired, "value")}, nil)
					return Created, err
				case diff.Update:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPatch,
						fmt.Sprintf("/repos/%s/%s/actions/variables/%s", repo.Owner, repo.Name, name),
						map[string]any{"name": name, "value": stringField(d.Desired, "value")}, nil)
					return Updated, err
				case diff.Delete:
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
						fmt.Sprintf("/repos/%s/%s/actions/variables/%s", repo.Owner, repo.Name, name), nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "variables", "name", fetch, desired, mutate, dryRun)
		},
	}
}

// Deploy keys: GitHub only supports create + delete. Update == replace.
func NewDeployKeysLane(cfg *config.DeployKeysConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "deploy_keys",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveKey struct {
					ID       int    `json:"id"`
					Title    string `json:"title"`
					Key      string `json:"key"`
					ReadOnly bool   `json:"read_only"`
				}
				keys, err := ghclient.Paginate[liveKey](ctx, cl, ghclient.PriorityCronReconcile, repo.Owner,
					fmt.Sprintf("/repos/%s/%s/keys", repo.Owner, repo.Name))
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(keys))
				for _, k := range keys {
					out = append(out, map[string]any{
						"_id":       k.ID,
						"title":     k.Title,
						"read_only": k.ReadOnly,
					})
				}
				return out, nil
			}
			// Strip key field from desired since we don't compare it.
			items := make([]map[string]any, 0, len(cfg.DeployKeys))
			for _, k := range cfg.DeployKeys {
				items = append(items, map[string]any{
					"title":     k.Title,
					"read_only": k.ReadOnly,
					"_key":      k.Key, // private; not diffed
				})
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				title := stringField(d.Desired, "title")
				if title == "" {
					title = stringField(d.Current, "title")
				}
				switch d.Action {
				case diff.Create, diff.Update:
					body := map[string]any{
						"title":     title,
						"key":       d.Desired.(map[string]any)["_key"],
						"read_only": d.Desired.(map[string]any)["read_only"],
					}
					if d.Action == diff.Update {
						// Delete then create.
						id := intField(d.Current, "_id")
						if _, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
							fmt.Sprintf("/repos/%s/%s/keys/%d", repo.Owner, repo.Name, int(id)), nil, nil); err != nil {
							return Updated, err
						}
					}
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPost,
						fmt.Sprintf("/repos/%s/%s/keys", repo.Owner, repo.Name), body, nil)
					if d.Action == diff.Create {
						return Created, err
					}
					return Updated, err
				case diff.Delete:
					id := intField(d.Current, "_id")
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete,
						fmt.Sprintf("/repos/%s/%s/keys/%d", repo.Owner, repo.Name, int(id)), nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "deploy_keys", "title", fetch, items, mutate, dryRun)
		},
	}
}
