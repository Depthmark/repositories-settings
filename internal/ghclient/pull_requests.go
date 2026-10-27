package ghclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
)

// PullRequestHeadSHA returns the commit at the head of a pull request.
// An issue_comment payload carries no SHA, so a PR command looks it up
// to read the settings the pull request proposes.
func (c *Client) PullRequestHeadSHA(ctx context.Context, repo config.Repo, number int) (string, error) {
	pr, _, err := c.gh.PullRequests.Get(
		WithCall(ctx, PriorityPRCheck, repo.Owner, ghapi.RoutePullRequest),
		repo.Owner, repo.Name, number)
	if err != nil {
		return "", fmt.Errorf("getting pull request %d: %w", number, err)
	}
	sha := pr.GetHead().GetSHA()
	if sha == "" {
		return "", errors.New("pull request has no head SHA")
	}
	return sha, nil
}

// RepoPermission is a user's access to one repository. Permission is
// GitHub's legacy base role (admin, write, read or none), where maintain
// maps to write and triage to read; RoleName is the role actually
// assigned, custom roles included.
type RepoPermission struct {
	Permission string
	RoleName   string
}

// CollaboratorPermission looks up user's permission on repo. GitHub
// answers from the repository's own access model, so it reflects team
// grants and base roles that a payload's author_association does not.
func (c *Client) CollaboratorPermission(ctx context.Context, repo config.Repo, user string) (RepoPermission, error) {
	lvl, _, err := c.gh.Repositories.GetPermissionLevel(
		WithCall(ctx, PriorityPRCheck, repo.Owner, ghapi.RouteCollaboratorPermission),
		repo.Owner, repo.Name, user)
	if err != nil {
		return RepoPermission{}, fmt.Errorf("getting permission of %s: %w", user, err)
	}
	return RepoPermission{Permission: lvl.GetPermission(), RoleName: lvl.GetRoleName()}, nil
}
