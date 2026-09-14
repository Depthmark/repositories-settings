package applier

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v76/github"

	"github.com/Depthmark/repositories-settings/internal/conc"
	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghapi"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
)

// Branches: the legacy branch-protection API. Each pattern is its own
// resource at PUT /repos/:owner/:repo/branches/:branch/protection, and
// live state is fetched per pattern — 404 means the branch is
// unprotected.
//
// Two shapes are in play. GitHub's GET wraps most settings in
// {"enabled": bool} objects and returns full actor records under
// `restrictions`; its PUT takes bare booleans and bare logins.
// ghapi.DecodeBranchProtection projects the read shape onto the write
// shape before diffing — without it a correct configuration reports a
// change on every field of every run.
func NewBranchesLane(cfg *config.BranchesConfig) Lane {
	if cfg == nil {
		return Lane{}
	}
	return Lane{
		Resource: "branches",
		Run: func(ctx context.Context, cl *ghclient.Client, repo config.Repo, dryRun bool) ([]diff.Diff, []Result, error) {
			byPattern := make(map[string]config.BranchProtection, len(cfg.Branches))
			diffs := make([]diff.Diff, 0, len(cfg.Branches))
			for _, b := range cfg.Branches {
				byPattern[b.Pattern] = b
				live, _, err := cl.GH().Repositories.GetBranchProtection(
					readCtx(ctx, repo, ghapi.RouteBranchProtection), repo.Owner, repo.Name, b.Pattern)
				if err != nil {
					if !isNotFound(err) && !isBranchNotProtected(err) {
						return nil, nil, fmt.Errorf("get branch protection %q: %w", b.Pattern, err)
					}
					live = nil // unprotected branch
				}
				desired, derr := diff.ToMap(b)
				if derr != nil {
					return nil, nil, derr
				}
				delete(desired, "pattern")
				d := diff.SingleResource("branches."+b.Pattern, ghapi.DecodeProtection(live), desired)
				diffs = append(diffs, d)
			}
			if dryRun {
				return diffs, nil, nil
			}
			actionable := make([]diff.Diff, 0, len(diffs))
			for _, d := range diffs {
				if d.Action != diff.Noop {
					actionable = append(actionable, d)
				}
			}
			if len(actionable) == 0 {
				return diffs, nil, nil
			}
			results, _ := conc.Map(ctx, actionable, conc.DefaultApplierConcurrency,
				func(ctx context.Context, d diff.Diff) (Result, error) {
					pattern := strings.TrimPrefix(d.Resource, "branches.")
					return applyBranchProtection(ctx, cl, repo, d, byPattern[pattern]), nil
				})
			return diffs, results, nil
		},
	}
}

func applyBranchProtection(
	ctx context.Context,
	cl *ghclient.Client,
	repo config.Repo,
	d diff.Diff,
	cfg config.BranchProtection,
) Result {
	pattern := strings.TrimPrefix(d.Resource, "branches.")
	switch d.Action {
	case diff.Create, diff.Update:
		calls := 1
		wctx := writeCtx(ctx, repo, ghapi.RouteBranchProtection)
		_, _, err := cl.GH().Repositories.UpdateBranchProtection(
			wctx, repo.Owner, repo.Name, pattern, ghapi.EncodeProtection(cfg))
		if err != nil {
			return failure(d.Resource, Updated, err, calls)
		}
		// required_signatures is its own sub-resource, so it is written
		// only when the diff actually reported it changing.
		if want, changed := signaturesChange(d); changed {
			calls++
			sctx := writeCtx(ctx, repo, ghapi.RouteBranchSignatures)
			var serr error
			if want {
				_, _, serr = cl.GH().Repositories.RequireSignaturesOnProtectedBranch(sctx, repo.Owner, repo.Name, pattern)
			} else {
				_, serr = cl.GH().Repositories.OptionalSignaturesOnProtectedBranch(sctx, repo.Owner, repo.Name, pattern)
			}
			if serr != nil {
				return failure(d.Resource, Updated, fmt.Errorf("required_signatures: %w", serr), calls)
			}
		}
		if d.Action == diff.Create {
			return success(d.Resource, Created, calls)
		}
		return success(d.Resource, Updated, calls)
	case diff.Delete:
		_, err := cl.GH().Repositories.RemoveBranchProtection(
			writeCtx(ctx, repo, ghapi.RouteBranchProtection), repo.Owner, repo.Name, pattern)
		if err != nil {
			return failure(d.Resource, Deleted, err, 1)
		}
		return success(d.Resource, Deleted, 1)
	}
	return success(d.Resource, Skipped, 0)
}

// isBranchNotProtected recognises the 403 GitHub returns for a branch
// that exists but has no protection record. It is not an error state —
// it is the "create" case — but it is not a 404 either.
func isBranchNotProtected(err error) bool {
	var ge *github.ErrorResponse
	if !errorsAs(err, &ge) || ge.Response == nil {
		return false
	}
	return ge.Response.StatusCode == 403 && strings.Contains(ge.Message, "not protected")
}

// signaturesChange reports the desired required_signatures value and
// whether the diff says it needs writing.
func signaturesChange(d diff.Diff) (want bool, changed bool) {
	for _, c := range d.Changes {
		if c.Path == "required_signatures" {
			v, _ := c.To.(bool)
			return v, true
		}
	}
	return false, false
}
