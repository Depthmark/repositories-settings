// Package reconciler is the per-repo orchestrator. Mirrors
// src/github/reconciler.ts: Phase A repo+topics first (it gates
// archival/visibility downstream), then 13 resource lanes fan out in
// parallel via errgroup. Each lane's failure is captured as a synthetic
// ApplyResult so a single broken lane doesn't abort the reconcile.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Depthmark/repositories-settings/internal/applier"
	"github.com/Depthmark/repositories-settings/internal/config"
	"github.com/Depthmark/repositories-settings/internal/diff"
	"github.com/Depthmark/repositories-settings/internal/ghclient"
	"github.com/Depthmark/repositories-settings/internal/metrics"
)

// Report is the per-reconcile outcome returned to callers
// (HTTP /api/reconcile, cron worker reports).
type Report struct {
	Repo      config.Repo      `json:"repo"`
	Timestamp time.Time        `json:"timestamp"`
	Trigger   config.Trigger   `json:"trigger"`
	Diffs     []diff.Diff      `json:"diffs"`
	Applied   []applier.Result `json:"applied"`
	DryRun    bool             `json:"dry_run"`
	Duration  time.Duration    `json:"duration"`
}

// Prefetched is optional repo state populated by the cron worker via
// batch GraphQL — passed in to skip a REST call in Phase A.
type Prefetched struct {
	Repo *applier.PrefetchedRepo
}

// Reconciler is constructed once and reused across reconciles.
type Reconciler struct {
	logger *slog.Logger
}

func New(logger *slog.Logger) *Reconciler {
	return &Reconciler{logger: logger}
}

// Reconcile runs the phased reconcile for one repo.
func (r *Reconciler) Reconcile(
	ctx context.Context,
	cl *ghclient.Client,
	repo config.Repo,
	settings *config.Settings,
	trigger config.Trigger,
	dryRun bool,
	prefetched *Prefetched,
) (*Report, error) {
	log := r.logger.With("owner", repo.Owner, "repo", repo.Name, "trigger", string(trigger))
	start := time.Now()
	rep := &Report{
		Repo:      repo,
		Timestamp: start.UTC(),
		Trigger:   trigger,
		DryRun:    dryRun,
	}
	log.Info("reconcile start", "dry_run", dryRun)

	if settings == nil {
		settings = &config.Settings{}
	}

	// Phase A: repo + topics (sequential — gates archival / visibility).
	if settings.Repo != nil || settings.Topics != nil {
		var pf *applier.PrefetchedRepo
		if prefetched != nil {
			pf = prefetched.Repo
		}
		lane := applier.NewRepoLane(settings, pf)
		ds, ap := r.runLane(ctx, lane, cl, repo, dryRun, log)
		rep.Diffs = append(rep.Diffs, ds...)
		rep.Applied = append(rep.Applied, ap...)
	}

	// Phase B: parallel lanes for the rest. Build only the lanes we
	// have config for so we don't spend goroutines on unmanaged
	// resources.
	lanes := []applier.Lane{}
	if l := applier.NewTeamsLane(settings.Teams); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewRulesetsLane(settings.Rulesets); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewEnvironmentsLane(settings.Environments); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewWebhooksLane(settings.Webhooks); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewAutolinksLane(settings.Autolinks); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewActionsLane(settings.Actions); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewSecurityLane(settings.Security); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewPagesLane(settings.Pages); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewSecretsLane(settings.Secrets); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewVariablesLane(settings.Variables); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewDeployKeysLane(settings.DeployKeys); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewCustomPropertiesLane(settings.CustomProperties); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewCollaboratorsLane(settings.Collaborators); l.Run != nil {
		lanes = append(lanes, l)
	}
	if l := applier.NewBranchesLane(settings.Branches); l.Run != nil {
		lanes = append(lanes, l)
	}

	type laneOut struct {
		ds []diff.Diff
		ap []applier.Result
	}
	results := make([]laneOut, len(lanes))
	var wg sync.WaitGroup
	for i, lane := range lanes {
		wg.Add(1)
		go func(i int, lane applier.Lane) {
			defer wg.Done()
			ds, ap := r.runLane(ctx, lane, cl, repo, dryRun, log)
			results[i] = laneOut{ds: ds, ap: ap}
		}(i, lane)
	}
	wg.Wait()

	for _, lo := range results {
		rep.Diffs = append(rep.Diffs, lo.ds...)
		rep.Applied = append(rep.Applied, lo.ap...)
	}

	rep.Duration = time.Since(start)
	status := "success"
	if dryRun {
		status = "dry_run"
	} else if hasFailure(rep.Applied) {
		status = "partial_failure"
	}
	metrics.ReconcileTotal.WithLabelValues(string(trigger), status).Inc()
	metrics.ReconcileDuration.WithLabelValues(string(trigger)).Observe(rep.Duration.Seconds())
	for _, ap := range rep.Applied {
		st := "success"
		if !ap.Success {
			st = "failure"
		}
		// Resource format is "<group>.<key>" or just "<group>". Use the
		// group prefix only so labels stay bounded.
		group := ap.Resource
		for j := 0; j < len(group); j++ {
			if group[j] == '.' {
				group = group[:j]
				break
			}
		}
		metrics.ApplierTotal.WithLabelValues(group, string(ap.Action), st).Inc()
	}
	log.Info("reconcile end",
		"diffs", actionable(rep.Diffs),
		"applied", len(rep.Applied),
		"failures", failures(rep.Applied),
		"duration_ms", rep.Duration.Milliseconds(),
		"status", status,
	)
	return rep, nil
}

// runLane is the per-lane body, with panic recovery so a buggy lane
// produces a synthetic failure rather than crashing the reconcile.
func (r *Reconciler) runLane(
	ctx context.Context,
	lane applier.Lane,
	cl *ghclient.Client,
	repo config.Repo,
	dryRun bool,
	log *slog.Logger,
) (diffs []diff.Diff, results []applier.Result) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Error("lane panicked", "lane", lane.Resource, "panic", fmt.Sprint(rec))
			results = []applier.Result{{
				Resource: lane.Resource,
				Action:   applier.Updated,
				Success:  false,
				Error:    fmt.Sprintf("panic: %v", rec),
			}}
		}
	}()
	ds, rs, err := lane.Run(ctx, cl, repo, dryRun)
	if err != nil {
		log.Error("lane failed", "lane", lane.Resource, "error", err)
		return ds, []applier.Result{{
			Resource: lane.Resource,
			Action:   applier.Updated,
			Success:  false,
			Error:    err.Error(),
		}}
	}
	return ds, rs
}

func hasFailure(rs []applier.Result) bool {
	for _, r := range rs {
		if !r.Success {
			return true
		}
	}
	return false
}

func actionable(ds []diff.Diff) int {
	n := 0
	for _, d := range ds {
		if d.Action != diff.Noop {
			n++
		}
	}
	return n
}

func failures(rs []applier.Result) int {
	n := 0
	for _, r := range rs {
		if !r.Success {
			n++
		}
	}
	return n
}
