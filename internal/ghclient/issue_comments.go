package ghclient

import (
	"context"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
)

// IssueComment is the subset of a PR comment the sticky-comment flow
// needs. It stays a local type rather than exposing go-github's, so the
// server package does not have to nil-check pointer accessors on every
// field it reads.
type IssueComment struct {
	ID   int64
	Body string
	User struct {
		Login string
		Type  string
	}
}

func fromSDKComment(c *github.IssueComment) IssueComment {
	out := IssueComment{ID: c.GetID(), Body: c.GetBody()}
	out.User.Login = c.GetUser().GetLogin()
	out.User.Type = c.GetUser().GetType()
	return out
}

// ListIssueComments walks every comment on an issue or PR.
func (c *Client) ListIssueComments(ctx context.Context, repo config.Repo, number int) ([]IssueComment, error) {
	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var out []IssueComment
	for {
		page, resp, err := c.gh.Issues.ListComments(
			WithCall(ctx, PriorityPRCheck, repo.Owner, ghapi.RouteIssueComments),
			repo.Owner, repo.Name, number, opts)
		if err != nil {
			return nil, err
		}
		for _, cm := range page {
			out = append(out, fromSDKComment(cm))
		}
		if resp == nil || resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// CreateIssueComment posts a new comment and returns it, mainly so the
// caller can stash the ID for later in-place updates.
func (c *Client) CreateIssueComment(ctx context.Context, repo config.Repo, number int, body string) (*IssueComment, error) {
	created, _, err := c.gh.Issues.CreateComment(
		WithCall(ctx, PriorityPRCheck, repo.Owner, ghapi.RouteIssueComments),
		repo.Owner, repo.Name, number, &github.IssueComment{Body: github.Ptr(body)})
	if err != nil {
		return nil, err
	}
	out := fromSDKComment(created)
	return &out, nil
}

// UpdateIssueComment edits a comment in place, so reruns refresh the one
// sticky comment instead of stacking new ones.
func (c *Client) UpdateIssueComment(ctx context.Context, repo config.Repo, commentID int64, body string) error {
	_, _, err := c.gh.Issues.EditComment(
		WithCall(ctx, PriorityPRCheck, repo.Owner, ghapi.RouteIssueCommentsID),
		repo.Owner, repo.Name, commentID, &github.IssueComment{Body: github.Ptr(body)})
	return err
}
