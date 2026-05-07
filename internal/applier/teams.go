package applier

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Teams: GET /repos/:owner/:repo/teams returns team objects with slug +
// permission. Manage with PUT /orgs/:org/teams/:slug/repos/:owner/:repo
// and DELETE.

func NewTeamsLane(cfg *config.TeamsConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "teams",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			fetch := func(ctx context.Context) ([]map[string]any, error) {
				type liveTeam struct {
					Slug       string `json:"slug"`
					Permission string `json:"permission"`
				}
				teams, err := ghclient.Paginate[liveTeam](ctx, cl, ghclient.PriorityCronReconcile, repo.Owner,
					fmt.Sprintf("/repos/%s/%s/teams", repo.Owner, repo.Name))
				if err != nil {
					return nil, err
				}
				out := make([]map[string]any, 0, len(teams))
				for _, t := range teams {
					out = append(out, map[string]any{"slug": t.Slug, "permission": t.Permission})
				}
				return out, nil
			}
			desired, err := diff.ToMaps(cfg.Teams)
			if err != nil {
				return nil, nil, err
			}
			mutate := func(ctx context.Context, d diff.Diff) (Action, error) {
				slug := stringField(d.Desired, "slug")
				if slug == "" {
					slug = stringField(d.Current, "slug")
				}
				switch d.Action {
				case diff.Create, diff.Update:
					perm := stringField(d.Desired, "permission")
					path := fmt.Sprintf("/orgs/%s/teams/%s/repos/%s/%s", repo.Owner, slug, repo.Owner, repo.Name)
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPut, path,
						map[string]any{"permission": perm}, nil)
					if d.Action == diff.Create {
						return Created, err
					}
					return Updated, err
				case diff.Delete:
					path := fmt.Sprintf("/orgs/%s/teams/%s/repos/%s/%s", repo.Owner, slug, repo.Owner, repo.Name)
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodDelete, path, nil, nil)
					return Deleted, err
				}
				return Skipped, nil
			}
			return namedListRun(ctx, "teams", "slug", fetch, desired, mutate, dryRun)
		},
	}
}

func stringField(v any, key string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// intField extracts a numeric field that may have been stored as int
// (set directly from a typed struct) or float64 (round-tripped through
// JSON). Returns 0 when the key is missing or holds an unsupported
// type. Used by appliers that key resources by GitHub's numeric ID.
func intField(v any, key string) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	switch n := m[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	}
	return 0
}
