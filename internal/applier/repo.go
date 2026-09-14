package applier

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
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
// in batch via GraphQL. Passing it lets the lane skip the REST GET —
// but only when it can answer everything the repository configures.
// See prefetchedRepoFields.
type PrefetchedRepo struct {
	Description, Homepage                           string
	Private, Archived                               bool
	HasIssues, HasProjects, HasWiki, HasDiscussions bool
	IsTemplate                                      bool
	Topics                                          []string
	// TopicsComplete is false when the batch query truncated the topic
	// list. A truncated list would be diffed as "these topics were
	// removed", so the lane falls back to REST instead.
	TopicsComplete bool
}

// prefetchedRepoFields are the liveRepo JSON keys the cron batch GraphQL
// query populates. The batch document is a strict subset of the REST one:
// every other key would arrive as its zero value, and because the diff is
// one-sided against desired, a configured allow_squash_merge: true would
// read as drift from false and be rewritten on every cron pass.
var prefetchedRepoFields = map[string]bool{
	"description":     true,
	"homepage":        true,
	"private":         true,
	"archived":        true,
	"has_issues":      true,
	"has_projects":    true,
	"has_wiki":        true,
	"has_discussions": true,
	"is_template":     true,
	"topics":          true,
}

// prefetchCovers reports whether the batch record can answer every field
// the repository configures.
func prefetchCovers(desired map[string]any) bool {
	for k := range desired {
		if !prefetchedRepoFields[k] {
			return false
		}
	}
	return true
}

// liveRepoFrom narrows a prefetched batch record to the live document
// shape. Only the fields in prefetchedRepoFields are populated.
func liveRepoFrom(p *PrefetchedRepo) liveRepo {
	return liveRepo{
		Description:    p.Description,
		Homepage:       p.Homepage,
		Private:        p.Private,
		Archived:       p.Archived,
		HasIssues:      p.HasIssues,
		HasProjects:    p.HasProjects,
		HasWiki:        p.HasWiki,
		HasDiscussions: p.HasDiscussions,
		IsTemplate:     p.IsTemplate,
		Topics:         p.Topics,
	}
}

// NewRepoLane builds the Phase A applier (repo settings + topics).
func NewRepoLane(cfg *config.Settings, prefetched *PrefetchedRepo) Lane {
	return Lane{
		Resource: "repository",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			if cfg.Repo == nil && cfg.Topics == nil {
				return nil, nil, nil
			}

			// Desired first: which fields are configured decides whether
			// the cron worker's partial record is usable at all.
			var desired map[string]any
			if cfg.Repo != nil {
				var err error
				desired, err = diff.ToMap(cfg.Repo)
				if err != nil {
					return nil, nil, err
				}
			}

			var live liveRepo
			if usablePrefetch(prefetched, desired, cfg.Topics != nil) {
				live = liveRepoFrom(prefetched)
			} else {
				r, _, err := cl.GH().Repositories.Get(
					readCtx(ctx, repo, ghapi.RouteRepo), repo.Owner, repo.Name)
				if err != nil {
					return nil, nil, fmt.Errorf("get repo: %w", err)
				}
				live = repoFromSDK(r)
			}

			var diffs []diff.Diff
			var applied []Result

			if cfg.Repo != nil {
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
					_, _, err := cl.GH().Repositories.Edit(
						writeCtx(ctx, repo, ghapi.RouteRepo), repo.Owner, repo.Name,
						ghapi.EncodeRepoPatch(patch))
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
					_, _, err := cl.GH().Repositories.ReplaceAllTopics(
						writeCtx(ctx, repo, ghapi.RouteRepoTopics), repo.Owner, repo.Name, des)
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

// usablePrefetch reports whether the cron worker's batch record is a
// complete answer for this repository's configuration. It is not enough
// that a record exists: the batch query fetches a subset of the fields
// and a bounded page of topics, and diffing against what it did not
// fetch reports drift that is not there.
func usablePrefetch(p *PrefetchedRepo, desired map[string]any, wantTopics bool) bool {
	if p == nil {
		return false
	}
	if !prefetchCovers(desired) {
		return false
	}
	if wantTopics && !p.TopicsComplete {
		return false
	}
	return true
}

// repoFromSDK narrows a go-github Repository to the fields this service
// manages. The SDK type carries well over a hundred fields; the diff is
// one-sided so extras would be harmless, but narrowing keeps the live
// document the same shape as the desired one and makes the comparison
// readable in a report.
func repoFromSDK(r *github.Repository) liveRepo {
	return liveRepo{
		Description:              r.GetDescription(),
		Homepage:                 r.GetHomepage(),
		Private:                  r.GetPrivate(),
		Visibility:               r.GetVisibility(),
		HasIssues:                r.GetHasIssues(),
		HasProjects:              r.GetHasProjects(),
		HasWiki:                  r.GetHasWiki(),
		HasDiscussions:           r.GetHasDiscussions(),
		IsTemplate:               r.GetIsTemplate(),
		AllowSquashMerge:         r.GetAllowSquashMerge(),
		AllowMergeCommit:         r.GetAllowMergeCommit(),
		AllowRebaseMerge:         r.GetAllowRebaseMerge(),
		AllowAutoMerge:           r.GetAllowAutoMerge(),
		DeleteBranchOnMerge:      r.GetDeleteBranchOnMerge(),
		AllowUpdateBranch:        r.GetAllowUpdateBranch(),
		SquashMergeCommitTitle:   r.GetSquashMergeCommitTitle(),
		SquashMergeCommitMessage: r.GetSquashMergeCommitMessage(),
		MergeCommitTitle:         r.GetMergeCommitTitle(),
		MergeCommitMessage:       r.GetMergeCommitMessage(),
		Archived:                 r.GetArchived(),
		WebCommitSignoffRequired: r.GetWebCommitSignoffRequired(),
		Topics:                   r.Topics,
	}
}
