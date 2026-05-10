package ghclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/Depthmark/repositories-settings/internal/config"
)

// BatchedRepoState is the prefetched live state used by the cron worker
// to skip a REST round-trip per repo. Values are pulled via aliased
// GraphQL queries (~50 repos per request).
type BatchedRepoState struct {
	Description    string
	Homepage       string
	IsPrivate      bool
	IsArchived     bool
	HasIssues      bool
	HasWiki        bool
	HasProjects    bool
	HasDiscussions bool
	IsTemplate     bool
	Topics         []string
}

// FetchBatchRepoSettings queries the given repos in a single GraphQL
// call (split into chunks of 50 transparently). Returns a map keyed by
// "owner/repo" for the repos that responded.
func (c *Client) FetchBatchRepoSettings(ctx context.Context, repos []config.Repo) (map[string]BatchedRepoState, error) {
	const chunkSize = 50
	out := make(map[string]BatchedRepoState, len(repos))
	for i := 0; i < len(repos); i += chunkSize {
		end := i + chunkSize
		if end > len(repos) {
			end = len(repos)
		}
		chunk := repos[i:end]
		if err := c.fetchBatchChunk(ctx, chunk, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Client) fetchBatchChunk(ctx context.Context, repos []config.Repo, out map[string]BatchedRepoState) error {
	if len(repos) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("query {\n")
	for i, r := range repos {
		fmt.Fprintf(&b, `  r%d: repository(owner: %q, name: %q) {
    description
    homepageUrl
    isPrivate
    isArchived
    hasIssuesEnabled
    hasWikiEnabled
    hasProjectsEnabled
    hasDiscussionsEnabled
    isTemplate
    repositoryTopics(first: 20) { nodes { topic { name } } }
  }`+"\n", i, r.Owner, r.Name)
	}
	b.WriteString("}\n")

	type repoNode struct {
		Description           string `json:"description"`
		HomepageURL           string `json:"homepageUrl"`
		IsPrivate             bool   `json:"isPrivate"`
		IsArchived            bool   `json:"isArchived"`
		HasIssuesEnabled      bool   `json:"hasIssuesEnabled"`
		HasWikiEnabled        bool   `json:"hasWikiEnabled"`
		HasProjectsEnabled    bool   `json:"hasProjectsEnabled"`
		HasDiscussionsEnabled bool   `json:"hasDiscussionsEnabled"`
		IsTemplate            bool   `json:"isTemplate"`
		RepositoryTopics      struct {
			Nodes []struct {
				Topic struct {
					Name string `json:"name"`
				} `json:"topic"`
			} `json:"nodes"`
		} `json:"repositoryTopics"`
	}
	rawData := map[string]*repoNode{}
	owner := repos[0].Owner
	if err := c.DoGraphQL(ctx, PriorityCronReconcile, owner, b.String(), nil, &rawData); err != nil {
		return err
	}
	for i, r := range repos {
		alias := fmt.Sprintf("r%d", i)
		n, ok := rawData[alias]
		if !ok || n == nil {
			continue
		}
		topics := make([]string, 0, len(n.RepositoryTopics.Nodes))
		for _, t := range n.RepositoryTopics.Nodes {
			topics = append(topics, t.Topic.Name)
		}
		out[r.String()] = BatchedRepoState{
			Description:    n.Description,
			Homepage:       n.HomepageURL,
			IsPrivate:      n.IsPrivate,
			IsArchived:     n.IsArchived,
			HasIssues:      n.HasIssuesEnabled,
			HasWiki:        n.HasWikiEnabled,
			HasProjects:    n.HasProjectsEnabled,
			HasDiscussions: n.HasDiscussionsEnabled,
			IsTemplate:     n.IsTemplate,
			Topics:         topics,
		}
	}
	return nil
}

// RulesetSummary identifies a repo's rulesets via the GraphQL list, so the
// caller can fetch detail in parallel via REST. Replaces the REST list
// endpoint (`GET /repos/{o}/{r}/rulesets`) and shifts the call to the
// GraphQL pool, which is otherwise idle for this lane.
type RulesetSummary struct {
	ID   int
	Name string
}

// FetchRepoRulesetIDs returns the databaseId+name of every repo-level
// ruleset in one GraphQL call. Bounded at 100 — repos with more than that
// are unrealistic; if it ever happens, the missing IDs are surfaced via
// the returned error so the caller can fall back to REST.
func (c *Client) FetchRepoRulesetIDs(ctx context.Context, prio Priority, repo config.Repo) ([]RulesetSummary, error) {
	const query = `query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    rulesets(first: 100) {
      nodes { databaseId name }
      pageInfo { hasNextPage }
    }
  }
}`
	var resp struct {
		Repository *struct {
			Rulesets struct {
				Nodes []struct {
					DatabaseID int    `json:"databaseId"`
					Name       string `json:"name"`
				} `json:"nodes"`
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
			} `json:"rulesets"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": repo.Owner, "name": repo.Name}
	if err := c.DoGraphQL(ctx, prio, repo.Owner, query, vars, &resp); err != nil {
		return nil, err
	}
	if resp.Repository == nil {
		return nil, nil
	}
	nodes := resp.Repository.Rulesets.Nodes
	out := make([]RulesetSummary, 0, len(nodes))
	for _, n := range nodes {
		if n.DatabaseID == 0 {
			continue
		}
		out = append(out, RulesetSummary{ID: n.DatabaseID, Name: n.Name})
	}
	if resp.Repository.Rulesets.PageInfo.HasNextPage {
		return out, fmt.Errorf("ghclient: %s/%s has >100 rulesets; pagination not implemented", repo.Owner, repo.Name)
	}
	return out, nil
}
