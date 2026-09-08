// Package worker is the cron batch reconciler. It batches repos in
// groups of 50, prefetches their repo state via a single GraphQL query
// per batch, and dispatches each repo to the reconciler concurrently.
package worker

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Depthmark/repositories-settings/internal/applier"
	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/reconciler"
)

const defaultBatchSize = 50

// Worker drives batch reconciles. Reuse it across runs.
type Worker struct {
	rec    *reconciler.Reconciler
	cl     *ghclient.Client
	logger *slog.Logger

	BatchSize   int
	Concurrency int
}

func New(rec *reconciler.Reconciler, cl *ghclient.Client, logger *slog.Logger) *Worker {
	return &Worker{
		rec:         rec,
		cl:          cl,
		logger:      logger,
		BatchSize:   defaultBatchSize,
		Concurrency: 8,
	}
}

// ReconcileBatch processes the given repos. settingsFor returns each
// repo's resolved configuration (org + suborg + repo, plus the policy
// verdict); the worker is agnostic to where it comes from.
func (w *Worker) ReconcileBatch(
	ctx context.Context,
	repos []config.Repo,
	settingsFor func(config.Repo) (*config.Resolution, error),
	dryRun bool,
) []*reconciler.Report {
	if len(repos) == 0 {
		return nil
	}
	batchSize := w.BatchSize
	if batchSize == 0 {
		batchSize = defaultBatchSize
	}
	concurrency := w.Concurrency
	if concurrency == 0 {
		concurrency = 8
	}

	out := make([]*reconciler.Report, 0, len(repos))
	var outMu sync.Mutex

	for i := 0; i < len(repos); i += batchSize {
		end := i + batchSize
		if end > len(repos) {
			end = len(repos)
		}
		batch := repos[i:end]

		prefetched, err := w.cl.FetchBatchRepoSettings(ctx, batch)
		if err != nil {
			w.logger.Warn("batch prefetch failed; falling back to per-repo REST", "error", err)
			prefetched = nil
		}

		// Bound concurrency within a batch.
		sem := make(chan struct{}, concurrency)
		var wg sync.WaitGroup
		for _, repo := range batch {
			repo := repo
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				rep, err := w.runOne(ctx, repo, prefetched, settingsFor, dryRun)
				if err != nil {
					w.logger.Warn("reconcile failed", "repo", repo.String(), "error", err)
					return
				}
				outMu.Lock()
				out = append(out, rep)
				outMu.Unlock()
			}()
		}
		wg.Wait()
	}
	return out
}

func (w *Worker) runOne(
	ctx context.Context,
	repo config.Repo,
	prefetched map[string]ghclient.BatchedRepoState,
	settingsFor func(config.Repo) (*config.Resolution, error),
	dryRun bool,
) (*reconciler.Report, error) {
	res, err := settingsFor(repo)
	if err != nil {
		return nil, err
	}

	var pf *reconciler.Prefetched
	if prefetched != nil {
		if state, ok := prefetched[repo.String()]; ok {
			pf = &reconciler.Prefetched{
				Repo: &applier.PrefetchedRepo{
					Description:    state.Description,
					Homepage:       state.Homepage,
					Private:        state.IsPrivate,
					Archived:       state.IsArchived,
					HasIssues:      state.HasIssues,
					HasProjects:    state.HasProjects,
					HasWiki:        state.HasWiki,
					HasDiscussions: state.HasDiscussions,
					IsTemplate:     state.IsTemplate,
					Topics:         state.Topics,
					TopicsComplete: state.TopicsComplete,
				},
			}
		}
	}

	return w.rec.Reconcile(ctx, w.cl, repo, res, config.TriggerCron, dryRun, pf)
}
