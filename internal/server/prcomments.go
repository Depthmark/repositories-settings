package server

import (
	"context"
	"strings"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// stickyMarker tags every comment the bot manages so subsequent runs
// can find and update the same comment instead of stacking new ones.
// HTML comments are invisible in rendered markdown but trivially
// greppable in the raw body.
const stickyMarker = "<!-- repo-settings:sticky -->"

// upsertStickyComment posts body as a PR comment, replacing the bot's
// existing sticky comment if one is present. Returns the comment ID
// that was written/updated.
func upsertStickyComment(
	ctx context.Context,
	cl *ghclient.Client,
	repo config.Repo,
	prNumber int,
	body string,
) (int64, error) {
	full := stickyMarker + "\n" + body
	existing, err := findStickyComment(ctx, cl, repo, prNumber)
	if err != nil {
		return 0, err
	}
	if existing != nil {
		if err := cl.UpdateIssueComment(ctx, repo, existing.ID, full); err != nil {
			return 0, err
		}
		return existing.ID, nil
	}
	created, err := cl.CreateIssueComment(ctx, repo, prNumber, full)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

// findStickyComment returns the bot's existing sticky comment on the
// PR, or nil if none exists. We match purely on the marker — ownership
// is enforced by the App's installation token, so other users can't
// fake a sticky.
func findStickyComment(
	ctx context.Context,
	cl *ghclient.Client,
	repo config.Repo,
	prNumber int,
) (*ghclient.IssueComment, error) {
	comments, err := cl.ListIssueComments(ctx, repo, prNumber)
	if err != nil {
		return nil, err
	}
	for i := range comments {
		c := comments[i]
		if strings.Contains(c.Body, stickyMarker) {
			return &c, nil
		}
	}
	return nil, nil
}
