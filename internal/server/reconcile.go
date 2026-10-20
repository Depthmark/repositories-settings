package server

import (
	"context"

	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

// reconcile delegates to the reconciler, which owns per-repository
// serialization for every trigger path.
func reconcile(
	ctx context.Context,
	d Deps,
	repo config.Repo,
	res *config.Resolution,
	trigger config.Trigger,
	dryRun bool,
) (*reconciler.Report, error) {
	return d.Reconciler.Reconcile(ctx, d.Client, repo, res, trigger, dryRun, nil)
}
