package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

func NewCollaboratorsLane(cfg *config.CollaboratorsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "collaborators",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveColl struct {
					Login       string `json:"login"`
					RoleName    string `json:"role_name"`
					Permissions struct {
						Admin    bool `json:"admin"`
						Maintain bool `json:"maintain"`
						Push     bool `json:"push"`
						Triage   bool `json:"triage"`
						Pull     bool `json:"pull"`
					} `json:"permissions"`
				}
				colls, err := ghclient.Paginate[liveColl](ctx, cl, ghclient.PriorityCronReconcile, repo.Owner,
					fmt.Sprintf("/repos/%s/%s/collaborators?affiliation=direct", repo.Owner, repo.Name))
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(colls))
				for _, c := range colls {
					perm := c.RoleName
					if perm == "" {
						switch {
						case c.Permissions.Admin:
							perm = "admin"
						case c.Permissions.Maintain:
							perm = "maintain"
						case c.Permissions.Push:
							perm = "push"
						case c.Permissions.Triage:
							perm = "triage"
						default:
							perm = "pull"
						}
					}
					out = append(out, map[string]any{"username": c.Login, "permission": perm})
				}
				return out, nil
			}
			desired, err := diff.ToMaps(cfg.Collaborators)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				user := stringField(d.Desired, "username")
				if user == "" {
					user = stringField(d.Current, "username")
				}
				path := fmt.Sprintf("/repos/%s/%s/collaborators/%s", repo.Owner, repo.Name, user)
				switch d.Action {
				case diff.Create, diff.Update:
					perm := stringField(d.Desired, "permission")
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPut, path,
						map[string]any{"permission": perm}, nil)
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
			return namedListRun(ctx, "collaborators", "username", fetch, desired, mutate, dryRun)
		},
	}
}
