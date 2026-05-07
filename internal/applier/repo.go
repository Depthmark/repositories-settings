package applier

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// liveRepo mirrors the subset of GET /repos/:owner/:repo we use when
// diffing. JSON tags match GitHub field names so they line up with the
// desired-config map keys.
type liveRepo struct {
	Description              string   `json:"description"`
	Homepage                 string   `json:"homepage"`
	Private                  bool     `json:"private"`
	Visibility               string   `json:"visibility"`
	HasIssues                bool     `json:"has_issues"`
	HasProjects              bool     `json:"has_projects"`
	HasWiki                  bool     `json:"has_wiki"`
	HasDiscussions           bool     `json:"has_discussions"`
	IsTemplate               bool     `json:"is_template"`
	AllowSquashMerge         bool     `json:"allow_squash_merge"`
	AllowMergeCommit         bool     `json:"allow_merge_commit"`
	AllowRebaseMerge         bool     `json:"allow_rebase_merge"`
	AllowAutoMerge           bool     `json:"allow_auto_merge"`
	DeleteBranchOnMerge      bool     `json:"delete_branch_on_merge"`
	AllowUpdateBranch        bool     `json:"allow_update_branch"`
	SquashMergeCommitTitle   string   `json:"squash_merge_commit_title"`
	SquashMergeCommitMessage string   `json:"squash_merge_commit_message"`
	MergeCommitTitle         string   `json:"merge_commit_title"`
	MergeCommitMessage       string   `json:"merge_commit_message"`
	Archived                 bool     `json:"archived"`
	WebCommitSignoffRequired bool     `json:"web_commit_signoff_required"`
	Topics                   []string `json:"topics"`
}

// PrefetchedRepo is the subset of repo state the cron worker can fetch
// in batch via GraphQL. Passing it skips the REST GET in the lane.
type PrefetchedRepo struct {
	Description, Homepage                           string
	Private, Archived                               bool
	HasIssues, HasProjects, HasWiki, HasDiscussions bool
	IsTemplate                                      bool
	Topics                                          []string
}

// NewRepoLane builds the Phase A applier (repo settings + topics).
func NewRepoLane(cfg *config.Settings, prefetched *PrefetchedRepo) Lane {
	return Lane{
		Resource: "repository",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			if cfg.Repo == nil && cfg.Topics == nil {
				return nil, nil, nil
			}
			var live liveRepo
			if prefetched != nil {
				live = liveRepo{
					Description:    prefetched.Description,
					Homepage:       prefetched.Homepage,
					Private:        prefetched.Private,
					Archived:       prefetched.Archived,
					HasIssues:      prefetched.HasIssues,
					HasProjects:    prefetched.HasProjects,
					HasWiki:        prefetched.HasWiki,
					HasDiscussions: prefetched.HasDiscussions,
					IsTemplate:     prefetched.IsTemplate,
					Topics:         prefetched.Topics,
				}
			} else {
				if _, err := cl.DoREST(ctx, ghclient.PriorityCronReconcile, repo.Owner, http.MethodGet,
					fmt.Sprintf("/repos/%s/%s", repo.Owner, repo.Name), nil, &live); err != nil {
					return nil, nil, fmt.Errorf("get repo: %w", err)
				}
			}

			var diffs []diff.Diff
			var applied []Result

			if cfg.Repo != nil {
				desired, err := diff.ToMap(cfg.Repo)
				if err != nil {
					return nil, nil, err
				}
				liveMap, _ := diff.ToMap(live)
				// Restrict liveMap to keys present in desired so we only
				// diff managed fields (matches TS behaviour).
				trimmed := map[string]any{}
				for k := range desired {
					trimmed[k] = liveMap[k]
				}
				d := diff.SingleResource("repository", trimmed, desired)
				diffs = append(diffs, d)
				if !dryRun && d.Action != diff.Noop {
					patch := map[string]any{}
					for _, ch := range d.Changes {
						patch[ch.Path] = ch.To
					}
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPatch,
						fmt.Sprintf("/repos/%s/%s", repo.Owner, repo.Name), patch, nil)
					if err != nil {
						applied = append(applied, failure("repository", Updated, err, 1))
					} else {
						applied = append(applied, success("repository", Updated, 1))
					}
				}
			}

			if cfg.Topics != nil {
				cur := append([]string(nil), live.Topics...)
				des := append([]string(nil), (*cfg.Topics)...)
				sort.Strings(cur)
				sort.Strings(des)
				d := diff.SingleResource("topics",
					map[string]any{"names": cur},
					map[string]any{"names": des})
				diffs = append(diffs, d)
				if !dryRun && d.Action != diff.Noop {
					body := map[string]any{"names": des}
					_, err := cl.DoREST(ctx, ghclient.PriorityMergeApply, repo.Owner, http.MethodPut,
						fmt.Sprintf("/repos/%s/%s/topics", repo.Owner, repo.Name), body, nil)
					if err != nil {
						applied = append(applied, failure("topics", Updated, err, 1))
					} else {
						applied = append(applied, success("topics", Updated, 1))
					}
				}
			}

			return diffs, applied, nil
		},
	}
}
