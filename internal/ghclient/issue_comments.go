package ghclient

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Depthmark/repositories-settings/internal/config"
)

// IssueComment is the subset of /repos/:o/:r/issues/:n/comments items
// the server's sticky-comment lookup needs.
type IssueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	User struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
}

// ListIssueComments paginates through all comments on an issue/PR.
// Mirrors GET /repos/:o/:r/issues/:n/comments.
func (c *Client) ListIssueComments(ctx context.Context, repo config.Repo, number int) ([]IssueComment, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", repo.Owner, repo.Name, number)
	return Paginate[IssueComment](ctx, c, PriorityPRCheck, repo.Owner, path)
}

// CreateIssueComment posts a new comment. Returns the created comment
// (mainly so callers can stash the ID for later updates).
func (c *Client) CreateIssueComment(ctx context.Context, repo config.Repo, number int, body string) (*IssueComment, error) {
	var out IssueComment
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", repo.Owner, repo.Name, number)
	if _, err := c.DoREST(ctx, PriorityPRCheck, repo.Owner, http.MethodPost, path,
		map[string]any{"body": body}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateIssueComment edits an existing comment in place. Used by the
// sticky-comment flow so reruns refresh one comment instead of stacking.
func (c *Client) UpdateIssueComment(ctx context.Context, repo config.Repo, commentID int64, body string) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d", repo.Owner, repo.Name, commentID)
	_, err := c.DoREST(ctx, PriorityPRCheck, repo.Owner, http.MethodPatch, path,
		map[string]any{"body": body}, nil)
	return err
}
