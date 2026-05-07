package applier

import (
	"context"
	"fmt"

	"github.com/Depthmark/repositories-settings/internal/conc"
	"github.com/Depthmark/repositories-settings/internal/diff"
)

// namedListRun is the canonical body of a Lane.Run for a named-list
// applier (rulesets, teams, webhooks, ...). It fetches live state, plans
// against desired, and concurrently fans out mutations.
func namedListRun(
	ctx context.Context,
	resource, keyField string,
	fetchLive func(ctx context.Context) ([]map[string]any, error),
	desired []map[string]any,
	mutate func(ctx context.Context, d diff.Diff) (Action, error),
	dryRun bool,
) ([]diff.Diff, []Result, error) {
	live, err := fetchLive(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", resource, err)
	}
	diffs := diff.NamedList(resource, live, desired, keyField)
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
	results, _ := conc.Map(ctx, actionable, conc.DefaultApplierConcurrency, func(ctx context.Context, d diff.Diff) (Result, error) {
		act, err := mutate(ctx, d)
		if err != nil {
			if act == "" {
				act = actionFromDiff(d.Action)
			}
			return failure(d.Resource, act, err, 1), nil
		}
		return success(d.Resource, act, 1), nil
	})
	return diffs, results, nil
}

// singleResourceRun is the canonical body for singleton resources (repo,
// actions, security, pages, custom_properties).
func singleResourceRun(
	ctx context.Context,
	resource string,
	fetchLive func(ctx context.Context) (map[string]any, error),
	desired map[string]any,
	mutate func(ctx context.Context, d diff.Diff) (Action, error),
	dryRun bool,
) ([]diff.Diff, []Result, error) {
	if desired == nil {
		return nil, nil, nil
	}
	live, err := fetchLive(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", resource, err)
	}
	d := diff.SingleResource(resource, live, desired)
	if d.Action == diff.Noop {
		return []diff.Diff{d}, nil, nil
	}
	if dryRun {
		return []diff.Diff{d}, nil, nil
	}
	act, err := mutate(ctx, d)
	if err != nil {
		if act == "" {
			act = actionFromDiff(d.Action)
		}
		return []diff.Diff{d}, []Result{failure(d.Resource, act, err, 1)}, nil
	}
	return []diff.Diff{d}, []Result{success(d.Resource, act, 1)}, nil
}
