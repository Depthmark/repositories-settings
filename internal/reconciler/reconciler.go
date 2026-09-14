// Package reconciler is the per-repo orchestrator. Org policy is checked
// first and can refuse the whole run; then Phase A applies repo+topics
// (it gates archival and visibility downstream); then 14 resource lanes
// fan out in parallel, joined by a sync.WaitGroup. Each lane's failure is
// captured as a synthetic ApplyResult so a single broken lane does not
// abort the reconcile.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
//
// Summary is the FormatReportMarkdown rendering — verdict line, error
// table, per-action tables. It is populated on the apply path so callers
// (notably the GitHub Actions workflow) can write it straight into a
// step summary without re-implementing the formatter. /api/check returns
// the same string at the response top-level for backwards compatibility,
// so it is left empty there to avoid duplication.
type Report struct {
	Repo      config.Repo      `json:"repo"`
	Timestamp time.Time        `json:"timestamp"`
	Trigger   config.Trigger   `json:"trigger"`
	Diffs     []diff.Diff      `json:"diffs"`
	Applied   []applier.Result `json:"applied"`
	DryRun    bool             `json:"dry_run"`
	Duration  time.Duration    `json:"duration"`
	Summary   string           `json:"summary,omitempty"`

	// Violations is every org policy breach found in the desired state,
	// warnings included. It is populated on dry runs too, so a PR check
	// shows the contributor what an apply would refuse.
	Violations []config.PolicyViolation `json:"violations,omitempty"`
	// Blocked is true when org policy refused the run. No lane ran and
	// nothing was written; Violations says why.
	Blocked bool `json:"blocked,omitempty"`
}

// Prefetched is optional repo state populated by the cron worker via
// batch GraphQL — passed in to skip a REST call in Phase A.
type Prefetched struct {
	Repo *applier.PrefetchedRepo
}

// Reconciler is constructed once and reused across reconciles.
type Reconciler struct {
	logger *slog.Logger
	// Disabled is the operator-level deny-set. Lanes whose key appears
	// here are skipped during reconcile and surfaced as Skipped Results
	// in the report so users see what got ignored. Nil = nothing
	// disabled.
	Disabled config.DisabledResources
}

func New(logger *slog.Logger) *Reconciler {
	return &Reconciler{logger: logger}
}

// WithDisabled returns r with the given disabled set installed. Used
// at boot from main once DISABLED_RESOURCES has been parsed.
func (r *Reconciler) WithDisabled(d config.DisabledResources) *Reconciler {
	r.Disabled = d
	return r
}

// Reconcile runs the phased reconcile for one repo.
func (r *Reconciler) Reconcile(
	ctx context.Context,
	cl *ghclient.Client,
	repo config.Repo,
	res *config.Resolution,
	trigger config.Trigger,
	dryRun bool,
	prefetched *Prefetched,
) (*Report, error) {
	var (
		rep *Report
		err error
	)
	cl.RepoLock().With(repo.Owner, repo.Name, func() {
		rep, err = r.reconcile(ctx, cl, repo, res, trigger, dryRun, prefetched)
	})
	return rep, err
}

// reconcile contains the phased operation while Reconcile owns the
// per-repository serialization boundary shared by every trigger.
func (r *Reconciler) reconcile(
	ctx context.Context,
	cl *ghclient.Client,
	repo config.Repo,
	res *config.Resolution,
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

	settings := res.Desired()

	// Org policy is checked before anything is read or written. A
	// repository that is trying to unlock a field the org locked, or to
	// grant itself a permission above the org ceiling, must not get a
	// partial apply of the parts that happened to be legal.
	if res != nil {
		rep.Violations = res.Violations
	}
	for _, v := range rep.Violations {
		metrics.PolicyViolationsTotal.WithLabelValues(string(v.Severity), violationGroup(v.Field)).Inc()
	}
	if blocking := res.Blocking(); len(blocking) > 0 {
		rep.Blocked = true
		rep.Duration = time.Since(start)
		for _, v := range blocking {
			rep.Applied = append(rep.Applied, applier.Result{
				Resource: v.Field,
				Action:   applier.Skipped,
				Success:  false,
				Error:    v.Message + " (org policy: " + v.OrgPolicy + ")",
			})
		}
		metrics.ReconcileTotal.WithLabelValues(string(trigger), "policy_blocked").Inc()
		metrics.ReconcileDuration.WithLabelValues(string(trigger)).Observe(rep.Duration.Seconds())
		log.Warn("reconcile refused by org policy", "violations", len(blocking))
		return rep, nil
	}

	// Phase A: repo + topics (sequential — gates archival / visibility).
	// "repository" lane covers both repo settings and topics; disabling
	// it skips the whole phase and emits one synthetic Skipped Result
	// so the user sees their config wasn't applied.
	if settings.Repo != nil || settings.Topics != nil {
		if r.Disabled.Has("repository") {
			rep.Applied = append(rep.Applied, skippedResult("repository"))
			log.Info("phase A skipped (operator-disabled)", "lane", "repository")
		} else {
			var pf *applier.PrefetchedRepo
			if prefetched != nil {
				pf = prefetched.Repo
			}
			lane := applier.NewRepoLane(settings, pf)
			ds, ap := r.runLane(ctx, lane, cl, repo, dryRun, log)
			rep.Diffs = append(rep.Diffs, ds...)
			rep.Applied = append(rep.Applied, ap...)
		}
	}

	// Phase B: parallel lanes for the rest. We build a (key, lane) spec
	// list and let applyDisabled() filter it: if the key is in the
	// operator deny-set, the lane is replaced by a Skipped Result; if
	// the lane is unconfigured (Run == nil) it's dropped silently.
	specs := []laneSpec{
		{"teams", applier.NewTeamsLane(settings.Teams)},
		{"rulesets", applier.NewRulesetsLane(settings.Rulesets)},
		{"environments", applier.NewEnvironmentsLane(settings.Environments)},
		{"webhooks", applier.NewWebhooksLane(settings.Webhooks)},
		{"autolinks", applier.NewAutolinksLane(settings.Autolinks)},
		{"actions", applier.NewActionsLane(settings.Actions)},
		{"security", applier.NewSecurityLane(settings.Security)},
		{"pages", applier.NewPagesLane(settings.Pages)},
		{"secrets", applier.NewSecretsLane(settings.Secrets)},
		{"variables", applier.NewVariablesLane(settings.Variables)},
		{"deploy_keys", applier.NewDeployKeysLane(settings.DeployKeys)},
		{"custom_properties", applier.NewCustomPropertiesLane(settings.CustomProperties)},
		{"collaborators", applier.NewCollaboratorsLane(settings.Collaborators)},
		{"branches", applier.NewBranchesLane(settings.Branches)},
	}
	lanes := make([]applier.Lane, 0, len(specs))
	for _, s := range specs {
		if s.lane.Run == nil {
			continue
		}
		if r.Disabled.Has(s.key) {
			rep.Applied = append(rep.Applied, skippedResult(s.key))
			log.Info("lane skipped (operator-disabled)", "lane", s.key)
			continue
		}
		lanes = append(lanes, s.lane)
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

// violationGroup bounds the metric label to the resource group. A
// violation's Field can carry a team slug or a ruleset name, which is
// user-supplied and unbounded; using it raw would let one repository's
// configuration blow up the metric's cardinality.
func violationGroup(field string) string {
	if i := strings.IndexByte(field, '.'); i >= 0 {
		return field[:i]
	}
	return field
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

// laneSpec pairs a canonical resource key (used by the operator
// deny-set and the report's Skipped section) with the constructed
// Lane. Lanes whose Run is nil are unconfigured and dropped without
// any Result entry.
type laneSpec struct {
	key  string
	lane applier.Lane
}

// skippedResult is the synthetic Result we emit for a lane the
// operator has disabled. Success=true so the dry-run conclusion stays
// "neutral" instead of "failure"; the message goes in Error so it
// renders consistently in the Skipped section of FormatReportMarkdown.
func skippedResult(key string) applier.Result {
	return applier.Result{
		Resource: key,
		Action:   applier.Skipped,
		Success:  true,
		Error:    "disabled by operator policy",
	}
}
